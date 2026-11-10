// Package engine reconciles managed repositories, provisions mirrors and
// publishes notifications to Matrix.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"git.nekocwd.link/neko/persia/internal/config"
	"git.nekocwd.link/neko/persia/internal/forge"
	"git.nekocwd.link/neko/persia/internal/matrix"
	"git.nekocwd.link/neko/persia/internal/message"
	"git.nekocwd.link/neko/persia/internal/store"
)

type remoteTarget struct {
	config config.Target
	remote forge.Remote
}

// Status is a snapshot of the last synchronization.
type Status struct {
	LastSync     time.Time
	LastError    string
	ManagedRepos int
}

// RepoMeta is cosmetic repository metadata shown on landing pages. It is
// cached in memory only and refreshed on every sync.
type RepoMeta struct {
	Description string
	AvatarURL   string
}

// Engine is the Persia reconciler.
type Engine struct {
	cfg     *config.Config
	local   *forge.Forgejo
	targets map[string]remoteTarget
	matrix  *matrix.Client
	store   *store.Store
	msgs    *message.Set
	log     *slog.Logger

	mu             sync.Mutex
	status         Status
	managementRoom string
	meta           map[string]RepoMeta
}

// New builds an engine.
func New(
	cfg *config.Config,
	local *forge.Forgejo,
	remotes map[string]forge.Remote,
	mx *matrix.Client,
	st *store.Store,
	msgs *message.Set,
	log *slog.Logger,
) *Engine {
	targets := make(map[string]remoteTarget, len(remotes))
	for name, remote := range remotes {
		targets[name] = remoteTarget{config: cfg.Targets[name], remote: remote}
	}
	return &Engine{
		cfg:     cfg,
		local:   local,
		targets: targets,
		matrix:  mx,
		store:   st,
		msgs:    msgs,
		log:     log,
		meta:    make(map[string]RepoMeta),
	}
}

// Metadata returns a snapshot of repository metadata discovered on the last
// sync, keyed by owner/name. It is kept in memory only: the data is cosmetic
// and cheap to refill on the next pass.
func (e *Engine) Metadata() map[string]RepoMeta {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]RepoMeta, len(e.meta))
	for key, value := range e.meta {
		out[key] = value
	}
	return out
}

func (e *Engine) setRepoMeta(repos []forge.Repo) {
	next := make(map[string]RepoMeta, len(repos))
	for _, repo := range repos {
		if repo.Description == "" && repo.AvatarURL == "" {
			continue
		}
		next[repo.FullName] = RepoMeta{
			Description: repo.Description,
			AvatarURL:   repo.AvatarURL,
		}
	}
	e.mu.Lock()
	e.meta = next
	e.mu.Unlock()
}

// Status returns the last synchronization status.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

func (e *Engine) setStatus(managed int, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status.LastSync = time.Now().UTC()
	e.status.ManagedRepos = managed
	if err != nil {
		e.status.LastError = err.Error()
	} else {
		e.status.LastError = ""
	}
}

// Run synchronizes periodically until the context is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	interval := e.cfg.SyncInterval.Std()
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			if err := e.Sync(ctx); err != nil {
				e.log.Error("sync failed", "error", err)
			}
			timer.Reset(interval)
		}
	}
}

// Sync performs one reconciliation pass.
func (e *Engine) Sync(ctx context.Context) error {
	repos, err := e.local.ListManagedRepos(ctx, e.cfg.Topic, e.cfg.Forgejo.Owners)
	if err != nil {
		e.setStatus(0, err)
		return err
	}

	e.log.Info("managed repositories discovered", "count", len(repos), "topic", e.cfg.Topic)
	e.setRepoMeta(repos)
	if mgmtRoom := e.ensureManagementRoom(ctx); mgmtRoom != "" {
		e.log.Info("management room ready", "room_id", mgmtRoom)
	}

	// Clean up state for repositories that used to be managed but no longer
	// carry the managed topic. This removes push mirrors, deploy keys and
	// local SQLite state (Matrix rooms themselves cannot be deleted by the
	// bot, so Persia at least leaves them with a final message).
	managed := make(map[string]bool, len(repos))
	for _, repo := range repos {
		managed[repo.FullName] = true
	}
	if repoKeys, err := e.store.RepoKeys(ctx); err == nil {
		for _, key := range repoKeys {
			if managed[key] {
				continue
			}
			if err := e.unmanageRepo(ctx, key); err != nil {
				e.log.Error("cannot unmanage repository", "repository", key, "error", err)
			}
		}
	}
	for _, repo := range repos {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := e.reconcileRepo(ctx, repo); err != nil {
			e.log.Error("reconcile failed", "repository", repo.FullName, "error", err)
		}
	}

	e.setStatus(len(repos), nil)
	return nil
}

func (e *Engine) reconcileRepo(ctx context.Context, repo forge.Repo) error {
	roomID, err := e.ensureRoom(ctx, repo)
	if err != nil {
		e.log.Warn("cannot create matrix room", "repository", repo.FullName, "error", err)
	}

	targets := e.targetsFor(repo)
	refs := map[string]forge.Ref{
		"local": {Owner: repo.Owner, Name: repo.Name, HTMLURL: repo.HTMLURL},
	}

	for _, name := range targets {
		target, ok := e.targets[name]
		if !ok {
			continue
		}

		ref, err := target.remote.EnsureRepo(ctx, repo)
		if err != nil {
			e.log.Error("cannot ensure remote repository", "repository", repo.FullName, "target", name, "error", err)
			continue
		}
		refs[name] = ref

		if description := e.remoteDescription(target, repo, name); description != "" {
			if err := target.remote.UpdateRepoDescription(ctx, ref, description); err != nil {
				e.log.Warn("cannot update remote repository description", "repository", repo.FullName, "target", name, "error", err)
			}
		}

		if err := e.ensureMirror(ctx, repo, name, target, ref, roomID); err != nil {
			e.log.Error("cannot ensure push mirror", "repository", repo.FullName, "target", name, "error", err)
		}
	}

	// Notifications from the local instance and every successfully
	// provisioned remote are published into the same room.
	if err := e.pollIssues(ctx, "local", repo, refs["local"], roomID); err != nil {
		e.log.Warn("cannot poll local issues", "repository", repo.FullName, "error", err)
	}
	if err := e.pollPipelines(ctx, "local", repo, refs["local"], roomID); err != nil {
		e.log.Warn("cannot poll local pipelines", "repository", repo.FullName, "error", err)
	}

	for name, ref := range refs {
		if name == "local" {
			continue
		}
		if err := e.pollIssues(ctx, name, repo, ref, roomID); err != nil {
			e.log.Warn("cannot poll remote issues", "repository", repo.FullName, "target", name, "error", err)
		}
		if err := e.pollPipelines(ctx, name, repo, ref, roomID); err != nil {
			e.log.Warn("cannot poll remote pipelines", "repository", repo.FullName, "target", name, "error", err)
		}
	}

	return nil
}

func (e *Engine) ensureMirror(
	ctx context.Context,
	repo forge.Repo,
	targetName string,
	target remoteTarget,
	ref forge.Ref,
	roomID string,
) error {
	address := ""
	useSSH := target.config.IsSSHMirror()
	if useSSH {
		generated, err := target.config.SSHAddress(ref.Owner, ref.Name)
		if err != nil {
			return err
		}
		address = generated
	} else {
		address = target.config.HTTPMirrorAddress(ref.Owner, ref.Name)
	}
	syncOnCommit := target.config.SyncOnCommitEnabled(e.cfg)

	var mirror forge.PushMirror
	created := false
	found := false

	mirrors, err := e.local.ListPushMirrors(ctx, repo.Owner, repo.Name)
	if err != nil {
		return err
	}

	// Clean up any previously configured mirror with the same normalized
	// address but a different transport before reusing its slot. This keeps
	// us from ending up with duplicate SSH/HTTPS mirrors after changing
	// mirror_transport.
	for _, existing := range mirrors {
		if normalizeRemote(existing.RemoteAddress) != normalizeRemote(address) {
			continue
		}
		existingIsSSH := strings.HasPrefix(strings.ToLower(strings.TrimSpace(existing.RemoteAddress)), "ssh://") ||
			strings.HasPrefix(strings.ToLower(strings.TrimSpace(existing.RemoteAddress)), "git@")
		if existingIsSSH != useSSH {
			if err := e.local.DeletePushMirror(ctx, repo.Owner, repo.Name, existing.RemoteName); err != nil {
				e.log.Warn("cannot delete stale push mirror with different transport",
					"repository", repo.FullName, "target", targetName, "error", err)
				continue
			}
			e.log.Info("deleted stale push mirror with different transport",
				"repository", repo.FullName, "target", targetName, "remote_address", existing.RemoteAddress)
		}
	}
	// Refresh the mirror list after any transport cleanup above.
	mirrors, err = e.local.ListPushMirrors(ctx, repo.Owner, repo.Name)
	if err != nil {
		return err
	}
	for _, existing := range mirrors {
		remoteTransportIsSSH := strings.HasPrefix(strings.ToLower(strings.TrimSpace(existing.RemoteAddress)), "ssh://") ||
			strings.HasPrefix(strings.ToLower(strings.TrimSpace(existing.RemoteAddress)), "git@")
		if remoteTransportIsSSH != useSSH {
			continue
		}
		if normalizeRemote(existing.RemoteAddress) == normalizeRemote(address) {
			mirror = existing
			found = true
			break
		}
	}
	mirrorUsername := target.config.Username
	mirrorPassword := target.config.Token
	if !useSSH {
		if mirrorUsername == "" {
			mirrorUsername = "oauth2"
		}
	}
	var oldPublicKey string
	if found && mirror.SyncOnCommit != syncOnCommit {
		if err := e.local.DeletePushMirror(ctx, repo.Owner, repo.Name, mirror.RemoteName); err != nil {
			return err
		}
		e.log.Info("push mirror recreated to apply sync_on_commit",
			"repository", repo.FullName, "target", targetName, "sync_on_commit", syncOnCommit)
		e.sendEvent(ctx, e.managementRoomID(ctx), "mirror_recreated", message.Data{
			Repo:         repo.FullName,
			Target:       targetName,
			SyncOnCommit: syncOnCommit,
		})
		found = false
	}
	if !found {
		mirror, err = e.local.CreatePushMirror(ctx, repo.Owner, repo.Name, address, e.cfg.MirrorInterval, syncOnCommit, useSSH, mirrorUsername, mirrorPassword)
		if err != nil {
			// A concurrent run or a previous partial failure may have created
			// it in the meantime.
			if mirrors, listErr := e.local.ListPushMirrors(ctx, repo.Owner, repo.Name); listErr == nil {
				for _, existing := range mirrors {
					if normalizeRemote(existing.RemoteAddress) == normalizeRemote(address) {
						mirror = existing
						found = true
						break
					}
				}
			}
			if !found {
				return err
			}
		} else {
			created = true
		}
	}

	trigger := created
	if useSSH && mirror.PublicKey != "" {
		nameTemplate := target.config.DeployKeyName
		if strings.TrimSpace(nameTemplate) == "" {
			nameTemplate = e.cfg.DeployKeyName
		}
		keyTitle := deployKeyTitle(nameTemplate, targetName, repo, ref)
		added, err := target.remote.EnsureDeployKey(ctx, ref, keyTitle, mirror.PublicKey)
		if errors.Is(err, forge.ErrDeployKeyExistsAsUser) {
			e.log.Warn("push mirror deploy key is already registered as a user SSH key",
				"repository", repo.FullName, "target", targetName)
			e.sendEvent(ctx, e.managementRoomID(ctx), "deploy_key_exists_as_user", message.Data{
				Repo:   repo.FullName,
				Target: targetName,
			})
		} else if err != nil {
			return err
		} else {
			if added {
				trigger = true
				e.sendEvent(ctx, e.managementRoomID(ctx), "deploy_key_added", message.Data{
					Repo:     repo.FullName,
					Target:   targetName,
					KeyTitle: keyTitle,
				})
			}
		}
	}

	// When the mirror was recreated, the old SSH key is no longer used for
	// this mirror. Remove it from the remote repository when possible.
	if useSSH && oldPublicKey != "" && strings.TrimSpace(oldPublicKey) != strings.TrimSpace(mirror.PublicKey) {
		if removed, err := target.remote.RemoveDeployKey(ctx, ref, oldPublicKey); err != nil {
			e.log.Warn("cannot remove old deploy key after mirror recreation",
				"repository", repo.FullName, "target", targetName, "error", err)
		} else if removed {
			e.sendEvent(ctx, e.managementRoomID(ctx), "deploy_key_removed_stale", message.Data{
				Repo:   repo.FullName,
				Target: targetName,
			})
		}
	}

	if trigger {
		if err := e.local.TriggerPushMirror(ctx, repo.Owner, repo.Name); err != nil {
			e.log.Warn("cannot trigger push mirror sync", "repository", repo.FullName, "target", targetName, "error", err)
		}
	}

	e.checkMirrorState(ctx, repo, targetName, mirror, roomID)
	return nil
}

func (e *Engine) checkMirrorState(ctx context.Context, repo forge.Repo, targetName string, mirror forge.PushMirror, roomID string) {
	previous, err := e.store.Mirrors(ctx, repo.FullName)
	if err != nil {
		e.log.Warn("cannot read mirror state", "repository", repo.FullName, "error", err)
		return
	}
	prev, seen := previous[targetName]

	currentErr := strings.TrimSpace(mirror.LastError)
	switch {
	case currentErr != "" && currentErr != prev.LastError:
		e.sendEvent(ctx, roomID, "mirror_failed", message.Data{
			Repo:   repo.FullName,
			Target: targetName,
			Error:  currentErr,
		})
	case currentErr == "" && seen && prev.LastError != "":
		e.sendEvent(ctx, roomID, "mirror_recovered", message.Data{
			Repo:   repo.FullName,
			Target: targetName,
		})
	}

	if err := e.store.SaveMirror(ctx, store.Mirror{
		RepoKey:     repo.FullName,
		Target:      targetName,
		Address:     mirror.RemoteAddress,
		PublicKey:   mirror.PublicKey,
		LastError:   currentErr,
		LastUpdated: time.Now().UTC(),
	}); err != nil {
		e.log.Warn("cannot save mirror state", "repository", repo.FullName, "error", err)
	}
}

func (e *Engine) pollIssues(ctx context.Context, source string, repo forge.Repo, ref forge.Ref, roomID string) error {
	items, err := e.remoteFor(source).ListIssues(ctx, ref)
	if err != nil {
		return err
	}

	stream := "issues|" + source + "|" + repo.FullName
	seeded, err := e.store.IsSeeded(ctx, stream)
	if err != nil {
		return err
	}

	for _, item := range items {
		prev, found, err := e.store.GetItem(ctx, source, repo.FullName, item.Kind, item.Number)
		if err != nil {
			return err
		}

		if !found {
			if seeded || e.cfg.NotifyFirstSeen() {
				e.sendEvent(ctx, roomID, "new_issue", newIssueData(e.displaySource(source), repo.FullName, item))
			}
			if err := e.store.SaveItem(ctx, store.Item{
				Source:    source,
				RepoKey:   repo.FullName,
				Kind:      item.Kind,
				Number:    item.Number,
				State:     item.State,
				Title:     item.Title,
				Comments:  item.Comments,
				UpdatedAt: item.UpdatedAt,
			}); err != nil {
				return err
			}
			continue
		}

		if prev.State == item.State && prev.Title == item.Title && prev.Comments == item.Comments && prev.UpdatedAt.Equal(item.UpdatedAt) {
			continue
		}

		e.sendEvent(ctx, roomID, "updated_issue", updatedIssueData(e.displaySource(source), repo.FullName, prev, item))
		if err := e.store.SaveItem(ctx, store.Item{
			Source:    source,
			RepoKey:   repo.FullName,
			Kind:      item.Kind,
			Number:    item.Number,
			State:     item.State,
			Title:     item.Title,
			Comments:  item.Comments,
			UpdatedAt: item.UpdatedAt,
		}); err != nil {
			return err
		}
	}

	if !seeded {
		if err := e.store.MarkSeeded(ctx, stream); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) pollPipelines(ctx context.Context, source string, repo forge.Repo, ref forge.Ref, roomID string) error {
	pipelines, err := e.remoteFor(source).ListPipelines(ctx, ref)
	if err != nil {
		return err
	}

	stream := "pipelines|" + source + "|" + repo.FullName
	seeded, err := e.store.IsSeeded(ctx, stream)
	if err != nil {
		return err
	}

	for _, pipeline := range pipelines {
		prev, found, err := e.store.GetPipeline(ctx, source, repo.FullName, pipeline.ID)
		if err != nil {
			return err
		}

		if !found {
			if (seeded || e.cfg.NotifyFirstSeen()) && e.isFailure(pipeline.Status) {
				e.sendEvent(ctx, roomID, "pipeline_failed", pipelineFailedData(e.displaySource(source), repo.FullName, pipeline))
			}
		} else if prev.Status != pipeline.Status {
			if e.isFailure(pipeline.Status) {
				e.sendEvent(ctx, roomID, "pipeline_failed", pipelineFailedData(e.displaySource(source), repo.FullName, pipeline))
			} else if e.isFailure(prev.Status) {
				// Recovery is scoped to the same source (mirror or local),
				// so a failed run on gitlab cannot be "fixed" by a green
				// run on github: those are different streams.
				e.sendEvent(ctx, roomID, "pipeline_recovered", pipelineFailedData(e.displaySource(source), repo.FullName, pipeline))
			}
		} else {
			continue
		}

		if err := e.store.SavePipeline(ctx, store.Pipeline{
			Source:    source,
			RepoKey:   repo.FullName,
			ID:        pipeline.ID,
			Name:      pipeline.Name,
			Status:    pipeline.Status,
			URL:       pipeline.URL,
			Ref:       pipeline.Ref,
			SHA:       pipeline.SHA,
			UpdatedAt: pipeline.UpdatedAt,
		}); err != nil {
			return err
		}
	}

	if !seeded {
		if err := e.store.MarkSeeded(ctx, stream); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) displaySource(source string) string {
	if source != "local" {
		return source
	}
	u, err := url.Parse(e.cfg.Forgejo.BaseURL)
	if err == nil && u.Host != "" {
		return u.Host
	}
	return "local"
}

func (e *Engine) remoteFor(source string) forge.Remote {
	if source == "local" {
		// The local Forgejo client implements the same read methods as Remote.
		return e.local
	}
	return e.targets[source].remote
}

func (e *Engine) isFailure(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failure", "failed", "timed_out", "startup_failure", "action_required", "error":
		return true
	case "cancelled", "canceled":
		return e.cfg.NotifyCancelledEnabled()
	default:
		return false
	}
}

func (e *Engine) ensureRoom(ctx context.Context, repo forge.Repo) (string, error) {
	roomID, found, err := e.store.Room(ctx, repo.FullName)
	if err != nil {
		return "", err
	}
	if found {
		return roomID, nil
	}

	name := fmt.Sprintf("%s · %s", e.cfg.Matrix.RoomPrefix, repo.FullName)
	topic := "Distributed repository: " + repo.HTMLURL
	roomID, err = e.matrix.CreateRoom(ctx, name, topic, e.cfg.Matrix.InviteUsers, e.cfg.Matrix.PublicRooms)
	if roomID == "" {
		return "", err
	}
	if err != nil {
		// The room exists but at least one invite failed. Keep the room ID so
		// the next cycle does not create a duplicate; the user can be invited
		// manually if needed.
		e.log.Warn("matrix room created with invite errors", "repository", repo.FullName, "room_id", roomID, "error", err)
	}
	if err := e.store.SaveRoom(ctx, repo.FullName, roomID); err != nil {
		return roomID, err
	}

	for _, userID := range e.cfg.Matrix.InviteUsers {
		if userID == "" {
			continue
		}
		content := map[string]any{}
		_ = e.matrix.State(ctx, roomID, "m.room.power_levels", &content)
		users, _ := content["users"].(map[string]any)
		if users == nil {
			users = map[string]any{}
		}
		users[userID] = 100
		content["users"] = users
		if err := e.matrix.PutState(ctx, roomID, "m.room.power_levels", content); err != nil {
			e.log.Warn("cannot promote matrix user to co-founder",
				"repository", repo.FullName, "user", userID, "error", err)
		}
	}
	if alias := e.cfg.Matrix.RoomAliasFor(repo.Owner, repo.Name, repo.FullName); alias != "" {
		if err := e.matrix.SetRoomAlias(ctx, alias, roomID); err != nil {
			e.log.Warn("cannot set room alias", "repository", repo.FullName, "alias", alias, "error", err)
		}
	}

	e.log.Info("matrix room created", "repository", repo.FullName, "room_id", roomID)
	e.sendEvent(ctx, roomID, "room_created", message.Data{
		Repo:    repo.FullName,
		RepoURL: repo.HTMLURL,
	})
	e.sendEvent(ctx, e.managementRoomID(ctx), "room_provisioned", message.Data{
		Repo:   repo.FullName,
		RoomID: roomID,
	})
	return roomID, nil
}

func (e *Engine) send(ctx context.Context, roomID string, message matrix.Message) {
	if roomID == "" {
		return
	}
	if err := e.matrix.Send(ctx, roomID, message); err != nil {
		e.log.Warn("cannot send matrix message", "room_id", roomID, "error", err)
	}
}

func (e *Engine) targetsFor(repo forge.Repo) []string {
	if hasTopic(repo.Topics, e.cfg.NoMirrorTopic) {
		return nil
	}

	byLowerName := make(map[string]string, len(e.targets))
	for name := range e.targets {
		byLowerName[strings.ToLower(name)] = name
	}

	var explicit []string
	prefix := strings.ToLower(e.cfg.TargetTopicPrefix)
	seen := map[string]bool{}
	sawExplicit := false
	for _, topic := range repo.Topics {
		lower := strings.ToLower(topic)
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		sawExplicit = true
		name := strings.TrimPrefix(lower, prefix)
		if actual, ok := byLowerName[name]; ok && !seen[actual] {
			seen[actual] = true
			explicit = append(explicit, actual)
		}
	}
	sort.Strings(explicit)
	if sawExplicit {
		return explicit
	}

	if len(e.cfg.DefaultTargets) > 0 {
		result := make([]string, 0, len(e.cfg.DefaultTargets))
		for _, name := range e.cfg.DefaultTargets {
			if _, ok := e.targets[name]; ok && !seen[name] {
				seen[name] = true
				result = append(result, name)
			}
		}
		return result
	}

	result := make([]string, 0, len(e.targets))
	for name := range e.targets {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func hasTopic(topics []string, topic string) bool {
	for _, item := range topics {
		if strings.EqualFold(item, topic) {
			return true
		}
	}
	return false
}

func normalizeRemote(address string) string {
	value := strings.ToLower(strings.TrimSpace(address))
	value = strings.TrimSuffix(value, "/")
	value = strings.TrimSuffix(value, ".git")
	for _, prefix := range []string{"ssh://", "https://", "http://", "git://"} {
		value = strings.TrimPrefix(value, prefix)
	}
	if at := strings.Index(value, "@"); at >= 0 {
		value = value[at+1:]
	}
	// scp-like addresses use host:path, regular URLs use host/path.
	value = strings.Replace(value, ":", "/", 1)
	return value
}

func deployKeyTitle(template, targetName string, repo forge.Repo, ref forge.Ref) string {
	if strings.TrimSpace(template) == "" {
		template = "persia {repo}"
	}
	title := strings.TrimSpace(strings.NewReplacer(
		"{repo}", repo.FullName,
		"{owner}", repo.Owner,
		"{name}", repo.Name,
		"{target}", targetName,
		"{namespace}", ref.Owner,
		"{remote}", ref.Owner+"/"+ref.Name,
	).Replace(template))
	if title == "" {
		title = "persia " + repo.FullName
	}

	const maxLen = 80
	if runes := []rune(title); len(runes) > maxLen {
		title = strings.TrimSpace(string(runes[:maxLen]))
	}
	return title
}

func (e *Engine) managementRoomID(ctx context.Context) string {
	if managedRoomDisabled(e.cfg) {
		return ""
	}
	if existing := strings.TrimSpace(e.cfg.Matrix.ManagementRoom); existing != "" &&
		!strings.EqualFold(existing, "auto") &&
		!strings.EqualFold(existing, "disabled") {
		e.mu.Lock()
		e.managementRoom = existing
		e.mu.Unlock()
		return e.managementRoom
	}
	return e.ensureManagementRoom(ctx)
}

const managementRepoKey = "__persia_management__"

func managedRoomDisabled(cfg *config.Config) bool {
	return strings.EqualFold(strings.TrimSpace(cfg.Matrix.ManagementRoom), "disabled")
}

// ensureManagementRoom creates the Matrix room used for operational messages
// (deploy keys, mirror recreation, room provisioning) if it does not already
// exist and returns its ID. Disabled management rooms return an empty string.
func (e *Engine) ensureManagementRoom(ctx context.Context) string {
	if managedRoomDisabled(e.cfg) {
		return ""
	}
	if e.managementRoom != "" {
		return e.managementRoom
	}
	if roomID, ok, err := e.store.Room(ctx, managementRepoKey); err == nil && ok {
		e.mu.Lock()
		e.managementRoom = roomID
		e.mu.Unlock()
		return roomID
	}

	name := strings.TrimSpace(e.cfg.Matrix.ManagementRoomName)
	if name == "" {
		name = "Persia management"
	}
	roomID, err := e.matrix.CreateRoom(ctx, name, "Persia operational updates",
		e.cfg.Matrix.InviteUsers, false)
	if err != nil {
		e.log.Warn("cannot create management room", "error", err)
		return ""
	}
	if err := e.store.SaveRoom(ctx, managementRepoKey, roomID); err != nil {
		e.log.Warn("cannot store management room", "error", err)
	}
	e.mu.Lock()
	e.managementRoom = roomID
	e.mu.Unlock()
	e.sendEvent(ctx, roomID, "room_initialized", message.Data{})
	return roomID
}

// sendEvent renders a notification from the configured templates and sends it
// to the given room.
func (e *Engine) sendEvent(ctx context.Context, roomID, event string, data message.Data) {
	if roomID == "" {
		return
	}
	if data.TargetData == nil {
		// Mirror events carry the target explicitly; issue and pipeline
		// events carry it as the source. Local events match no target and
		// simply get no extra data.
		name := data.Target
		if name == "" {
			name = data.Source
		}
		data.TargetData = e.targetTemplateData(name)
	}
	msg, err := e.msgs.Render(event, data)
	if err != nil {
		e.log.Error("cannot render matrix message", "event", event, "error", err)
		return
	}
	if err := e.matrix.Send(ctx, roomID, msg); err != nil {
		e.log.Warn("cannot send matrix message", "room_id", roomID, "event", event, "error", err)
	}
}

// targetTemplateData returns a copy of the free-form template_data configured
// for a target, or nil when the name does not match a target or it has none.
func (e *Engine) targetTemplateData(name string) map[string]string {
	target, ok := e.cfg.Targets[name]
	if !ok || len(target.TemplateData) == 0 {
		return nil
	}
	out := make(map[string]string, len(target.TemplateData))
	for key, value := range target.TemplateData {
		out[key] = value
	}
	return out
}

func parseRemoteRepo(raw string) (string, string) {
	value := strings.TrimSpace(raw)
	if u, err := url.Parse(value); err == nil && u.Path != "" {
		value = u.Path
	}
	value = strings.Trim(value, "/")
	value = strings.TrimSuffix(value, ".git")
	parts := strings.Split(value, "/")
	if len(parts) < 2 {
		return "", ""
	}
	return parts[len(parts)-2], parts[len(parts)-1]
}

func splitRepoKey(key string) (string, string) {
	parts := strings.Split(strings.TrimSpace(key), "/")
	if len(parts) < 2 {
		return "", ""
	}
	return parts[len(parts)-2], parts[len(parts)-1]
}

// unmanageRepo tears down mirrors, deploy keys and stored state for a
// repository that is no longer managed.
func (e *Engine) unmanageRepo(ctx context.Context, repoKey string) error {
	roomID, _, _ := e.store.Room(ctx, repoKey)
	mirrors, _ := e.store.Mirrors(ctx, repoKey)

	localOwner, localName := splitRepoKey(repoKey)
	for _, mirror := range mirrors {
		if locals, err := e.local.ListPushMirrors(ctx, localOwner, localName); err == nil {
			for _, candidate := range locals {
				if normalizeRemote(candidate.RemoteAddress) == normalizeRemote(mirror.Address) {
					if err := e.local.DeletePushMirror(ctx, localOwner, localName, candidate.RemoteName); err != nil {
						e.log.Warn("cannot delete push mirror while unmanaging",
							"repository", repoKey, "error", err)
					}
				}
			}
		}

		remoteOwner, remoteName := parseRemoteRepo(mirror.Address)
		if target, ok := e.targets[mirror.Target]; ok && remoteOwner != "" && remoteName != "" && mirror.PublicKey != "" {
			if removed, err := target.remote.RemoveDeployKey(ctx, forge.Ref{Owner: remoteOwner, Name: remoteName}, mirror.PublicKey); err != nil {
				e.log.Warn("cannot remove deploy key while unmanaging",
					"repository", repoKey, "target", mirror.Target, "error", err)
			} else if removed {
				e.sendEvent(ctx, e.managementRoomID(ctx), "deploy_key_removed_unmanage", message.Data{
					Repo:   repoKey,
					Target: mirror.Target,
				})
			}
		}
	}

	if roomID != "" {
		e.sendEvent(ctx, roomID, "room_retired", message.Data{Repo: repoKey})
		if err := e.matrix.Leave(ctx, roomID); err != nil {
			e.log.Warn("cannot leave Matrix room while unmanaging",
				"repository", repoKey, "room_id", roomID, "error", err)
		}
	}

	if err := e.store.DeleteRepoState(ctx, repoKey); err != nil {
		return err
	}
	e.sendEvent(ctx, e.managementRoomID(ctx), "repo_retired", message.Data{Repo: repoKey})
	return nil
}

// remoteDescription renders the description Persia writes to a remote mirror
// repository: the local description followed by a link built from the target's
// configured template. An empty local description and an empty template yield
// an empty string, in which case the remote description is left untouched.
func (e *Engine) remoteDescription(target remoteTarget, repo forge.Repo, targetName string) string {
	parts := make([]string, 0, 2)
	if desc := strings.TrimSpace(repo.Description); desc != "" {
		parts = append(parts, desc)
	}
	if template := strings.TrimSpace(target.config.Description); template != "" {
		link := strings.NewReplacer(
			"{host}", "",
			"{namespace}", "",
			"{name}", repo.Name,
			"{remote}", "",
			"{target}", targetName,
			"{owner}", repo.Owner,
			"{repo}", repo.FullName,
			"{description}", repo.Description,
		).Replace(template)
		link = strings.TrimSpace(link)
		if link != "" {
			parts = append(parts, link)
		}
	}
	return strings.Join(parts, " — ")
}
