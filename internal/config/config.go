// Package config loads and validates the Persia configuration.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that can be expressed as a human readable
// string ("2m", "30s", "1h") in YAML.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Config is the top-level Persia configuration.
type Config struct {
	Forgejo ForgejoConfig `yaml:"forgejo"`
	Matrix  MatrixConfig  `yaml:"matrix"`

	// Topic marks repositories that Persia manages. It is matched against
	// Forgejo repository topics (not labels, which are per-repository).
	Topic string `yaml:"topic"`

	// TargetTopicPrefix is prepended to a target name to opt a single
	// repository in or out of a mirror target, for example
	// "persia-target-github".
	TargetTopicPrefix string `yaml:"target_topic_prefix"`

	// NoMirrorTopic disables all mirror targets for a repository while still
	// keeping issue and CI notifications enabled.
	NoMirrorTopic string `yaml:"no_mirror_topic"`

	// DefaultTargets are used when a repository has no explicit target
	// topics. When empty, all enabled targets are used.
	DefaultTargets []string `yaml:"default_targets"`

	// DeployKeyName is the default title for SSH deploy keys registered on
	// mirror targets. Placeholders: {repo}, {owner}, {name}, {target},
	// {namespace}, {remote}. Targets may override it.
	DeployKeyName string `yaml:"deploy_key_name"`

	// Targets maps a short name (github, gitlab, altlinux, ...) to a remote
	// forge configuration.
	Targets map[string]Target `yaml:"targets"`

	SyncInterval   Duration `yaml:"sync_interval"`
	RequestTimeout Duration `yaml:"request_timeout"`
	MirrorInterval string   `yaml:"mirror_interval"`

	// MirrorSyncOnCommit makes Forgejo push to the mirror immediately on
	// every commit instead of waiting for mirror_interval. Defaults to true.
	// Targets may override it.
	MirrorSyncOnCommit *bool `yaml:"mirror_sync_on_commit"`

	Database string `yaml:"database"`

	NotifyOnFirstSeen *bool `yaml:"notify_on_first_seen"`
	NotifyCancelled   *bool `yaml:"notify_cancelled"`

	LogLevel string `yaml:"log_level"`

	HealthAddr    string `yaml:"health_addr"`
	ShortlinkAddr string `yaml:"shortlink_addr"`
	// Go html/template file rendered for /$RepoName. Required when
	// shortlink is enabled.
	ShortlinkTemplate     string `yaml:"shortlink_template"`
	ShortlinkTemplatePath string `yaml:"shortlink_template_path"`
}

// ForgejoConfig describes the local (source of truth) Forgejo instance.
type ForgejoConfig struct {
	BaseURL  string `yaml:"base_url"`
	Token    string `yaml:"token"`
	Username string `yaml:"username"`
	// Owners limits discovery to repositories owned by these users or
	// organizations. Empty means no restriction.
	Owners []string `yaml:"owners"`
}

// MatrixConfig describes the Matrix bot account.
type MatrixConfig struct {
	Homeserver  string   `yaml:"homeserver"`
	AccessToken string   `yaml:"access_token"`
	UserID      string   `yaml:"user_id"`
	InviteUsers []string `yaml:"invite_users"`
	RoomPrefix  string   `yaml:"room_prefix"`

	// PublicRooms makes Persia create per-repository rooms as public rooms and
	// expose a matrix.to/join link when an alias is available on the server.
	PublicRooms bool `yaml:"public_rooms"`
	// RoomAliasTemplate is the proposed Matrix alias for repository rooms.
	// Placeholders: {owner}, {repo}, {repo_key}, {domain}.
	RoomAliasTemplate string `yaml:"room_alias"`
	// RoomLinkTemplate is the link exposed on public pages instead of
	// matrix.to. Placeholders: {alias}, {room_id}, {repo}.
	RoomLinkTemplate string `yaml:"room_link"`

	// ManagementRoom stores operational updates (deploy keys, mirror
	// recreation, room provisioning) in a private room separate from each
	// repository's notification room. Empty disables the management room;
	// when set to "auto" (or left empty), Persia creates one automatically.
	ManagementRoom     string `yaml:"management_room"`
	ManagementRoomName string `yaml:"management_room_name"`

	// MessageTemplates is a directory with Go templates rendered for every
	// Matrix notification. Each <event>.tmpl file defines "text" and "html"
	// templates. Persia refuses to start when the directory is missing or does
	// not provide every required event.
	MessageTemplates string `yaml:"message_templates"`
}

// Target describes one remote forge where repositories are mirrored to and
// monitored on.
type Target struct {
	Type string `yaml:"type"`
	// BaseURL is the API endpoint root:
	//   - forgejo: https://altlinux.space (the /api/v1 suffix is added)
	//   - github:  https://api.github.com
	//   - gitlab:  https://gitlab.com/api/v4
	BaseURL string `yaml:"base_url"`
	Token   string `yaml:"token"`
	// Username is the Forgejo username used for repo creation. For GitHub and
	// GitLab the authenticated user is detected automatically.
	Username string `yaml:"username"`
	// Namespace is the user or organization that owns the remote repository.
	// When empty, the authenticated user is used.
	Namespace string `yaml:"namespace"`
	// Visibility is either "private" or "public" (default: private).
	Visibility string `yaml:"visibility"`

	// SSH settings are only used to build the push mirror URL.
	SSHUser string `yaml:"ssh_user"`
	SSHHost string `yaml:"ssh_host"`
	SSHPort int    `yaml:"ssh_port"`

	// MirrorTransport selects how the push mirror connects. "ssh" (default)
	// uses SSH with a generated deploy key; "https" uses username/token.
	MirrorTransport string `yaml:"mirror_transport"`

	// DeployKeyName overrides the global deploy key title for this target.
	// Supports the same placeholders as config.deploy_key_name.
	DeployKeyName string `yaml:"deploy_key_name"`

	// RepoURLTemplate builds the browsable repository URL shown on landing
	// pages. Placeholders: {host}, {namespace}, {name}, {remote}, {target},
	// {owner}, {repo}. Host and path are derived from the configured push
	// mirror address, so the result follows the real remote namespace. When
	// empty, https://{host}/{remote} is used.
	RepoURLTemplate string `yaml:"repo_url_template"`

	// Description is an optional template copied onto the remote mirror
	// repository description. It supports the same placeholders as
	// repo_url_template, plus {description} (the local description). The
	// rendered value is appended to the local description. For example:
	//   description: "https://p.example.org/{name}"
	Description string `yaml:"description"`

	// TemplateData is free-form data passed to templates. Matrix messages see
	// it as .TargetData and shortlink pages as .Data on each mirror entry, so
	// a target can carry extra labels or links without code changes.
	TemplateData map[string]string `yaml:"template_data"`

	// SyncOnCommit overrides the global mirror_sync_on_commit setting for
	// this target.
	SyncOnCommit *bool `yaml:"mirror_sync_on_commit"`

	Disabled bool `yaml:"disabled"`
}

// NotifyFirstSeen reports whether pre-existing issues and pipelines should be
// announced when a repository is discovered for the first time.
func (c *Config) NotifyFirstSeen() bool {
	return c.NotifyOnFirstSeen != nil && *c.NotifyOnFirstSeen
}

// NotifyCancelledEnabled reports whether cancelled pipelines are treated as
// failures. Defaults to true.
func (c *Config) NotifyCancelledEnabled() bool {
	return c.NotifyCancelled == nil || *c.NotifyCancelled
}

// MirrorSyncOnCommitEnabled reports whether push mirrors should sync on every
// commit. Defaults to true.
func (c *Config) MirrorSyncOnCommitEnabled() bool {
	return c.MirrorSyncOnCommit == nil || *c.MirrorSyncOnCommit
}

// SyncOnCommitEnabled resolves the effective mirror_sync_on_commit value for
// this target, falling back to the global setting.
func (t Target) SyncOnCommitEnabled(c *Config) bool {
	if t.SyncOnCommit != nil {
		return *t.SyncOnCommit
	}
	return c.MirrorSyncOnCommitEnabled()
}

// Private reports whether newly created remote repositories should be private.
func (t Target) Private() bool {
	return strings.ToLower(strings.TrimSpace(t.Visibility)) != "public"
}

// private helper
func (t Target) MirrorMode() string {
	if t.MirrorTransport == "" {
		return "ssh"
	}
	return t.MirrorTransport
}

// IsSSHMirror reports...
func (t Target) HTTPMirrorAddress(namespace, name string) string {
	host := strings.TrimSpace(t.SSHHost)
	if host == "" {
		if u, err := url.Parse(t.BaseURL); err == nil && u.Hostname() != "" {
			host = u.Hostname()
		}
	}
	return fmt.Sprintf("https://%s/%s/%s.git", host, strings.Trim(namespace, "/"), strings.Trim(name, "/"))
}

func (t Target) IsSSHMirror() bool {
	return strings.ToLower(strings.TrimSpace(t.MirrorTransport)) != "https"
}

// RepoHost returns the web host of the target, derived from ssh_host or
// base_url.
func (t Target) RepoHost() string {
	if host := strings.TrimSpace(t.SSHHost); host != "" {
		return host
	}
	if u, err := url.Parse(t.BaseURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return ""
}

// ParseMirrorAddress splits a push mirror address into its host and
// repository path. It understands scp-like SSH addresses (git@host:path),
// ssh:// URLs and http(s):// URLs. The returned path has no leading slash and
// keeps any .git suffix.
func ParseMirrorAddress(address string) (host, repoPath string, ok bool) {
	value := strings.TrimSpace(address)
	if value == "" {
		return "", "", false
	}
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil || u.Hostname() == "" {
			return "", "", false
		}
		return u.Hostname(), strings.TrimPrefix(u.Path, "/"), true
	}
	// scp-like form: [user@]host:path
	if at := strings.LastIndex(value, "@"); at >= 0 {
		value = value[at+1:]
	}
	colon := strings.Index(value, ":")
	if colon <= 0 || colon == len(value)-1 {
		return "", "", false
	}
	return value[:colon], strings.TrimPrefix(value[colon+1:], "/"), true
}

// RepoURL renders the browsable repository URL for a push mirror. The host and
// repository path are taken from the mirror address, so the result reflects
// the actual namespace used by the remote forge. The template may use
// {host}, {namespace}, {name}, {remote}, {target}, {owner} and {repo}
// placeholders. When the template is empty, https://{host}/{path} is used.
func (t Target) RepoURL(template, targetName, localOwner, localName, address string) string {
	host, repoPath, parsed := ParseMirrorAddress(address)
	if !parsed || host == "" {
		host = t.RepoHost()
	}
	repoPath = strings.TrimSuffix(strings.Trim(repoPath, "/"), ".git")

	namespace, name := "", ""
	if repoPath != "" {
		if i := strings.LastIndex(repoPath, "/"); i >= 0 {
			namespace, name = repoPath[:i], repoPath[i+1:]
		} else {
			name = repoPath
		}
	}

	if strings.TrimSpace(template) == "" {
		if host == "" || repoPath == "" {
			return ""
		}
		return "https://" + host + "/" + repoPath
	}
	return strings.TrimSpace(strings.NewReplacer(
		"{host}", host,
		"{namespace}", namespace,
		"{name}", name,
		"{remote}", repoPath,
		"{target}", targetName,
		"{owner}", localOwner,
		"{repo}", localOwner+"/"+localName,
	).Replace(template))
}
func (t Target) SSHAddress(namespace, name string) (string, error) {
	host := strings.TrimSpace(t.SSHHost)
	if host == "" {
		u, err := url.Parse(t.BaseURL)
		if err != nil {
			return "", fmt.Errorf("cannot derive ssh host from base_url %q: %w", t.BaseURL, err)
		}
		host = u.Hostname()
	}
	if host == "" {
		return "", fmt.Errorf("cannot derive ssh host for target")
	}

	user := strings.TrimSpace(t.SSHUser)
	if user == "" {
		user = "git"
	}

	path := strings.Trim(namespace, "/") + "/" + strings.Trim(name, "/") + ".git"
	if t.SSHPort != 0 && t.SSHPort != 22 {
		return fmt.Sprintf("ssh://%s@%s:%d/%s", user, host, t.SSHPort, path), nil
	}
	return fmt.Sprintf("%s@%s:%s", user, host, path), nil
}

// Load reads a YAML configuration file, expands environment variables, applies
// defaults and validates the result.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	expanded := os.ExpandEnv(string(raw))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	c.Forgejo.BaseURL = strings.TrimRight(strings.TrimSpace(c.Forgejo.BaseURL), "/")
	c.Matrix.Homeserver = strings.TrimRight(strings.TrimSpace(c.Matrix.Homeserver), "/")

	if database := strings.TrimSpace(os.Getenv("PERSIA_DATABASE")); database != "" {
		c.Database = database
	}

	if c.Topic == "" {
		c.Topic = "persia-managed"
	}
	if strings.TrimSpace(c.DeployKeyName) == "" {
		c.DeployKeyName = "persia {repo}"
	}

	// Restrict discovery to the configured owners. When the list is empty,
	// fall back to the authenticated Forgejo user so that repositories of
	// other users never get mirrored by accident. Use "*" to disable the
	// restriction explicitly.
	owners := make([]string, 0, len(c.Forgejo.Owners))
	for _, owner := range c.Forgejo.Owners {
		if owner = strings.TrimSpace(owner); owner != "" {
			owners = append(owners, owner)
		}
	}
	if len(owners) == 0 && strings.TrimSpace(c.Forgejo.Username) != "" {
		owners = append(owners, strings.TrimSpace(c.Forgejo.Username))
	}
	c.Forgejo.Owners = owners

	if c.TargetTopicPrefix == "" {
		c.TargetTopicPrefix = "persia-target-"
	}
	if c.NoMirrorTopic == "" {
		c.NoMirrorTopic = "persia-no-mirror"
	}
	if c.SyncInterval <= 0 {
		c.SyncInterval = Duration(5 * time.Minute)
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = Duration(30 * time.Second)
	}
	if c.MirrorInterval == "" {
		c.MirrorInterval = "8h"
	}
	if c.Database == "" {
		c.Database = "persia.db"
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.Matrix.RoomPrefix == "" {
		c.Matrix.RoomPrefix = "Persia"
	}
	if c.Matrix.RoomLinkTemplate == "" {
		c.Matrix.RoomLinkTemplate = "https://matrix.to/#/{alias}"
	}
	if strings.TrimSpace(c.Matrix.ManagementRoomName) == "" {
		c.Matrix.ManagementRoomName = "Persia management"
	}
	if strings.TrimSpace(c.Matrix.MessageTemplates) == "" {
		c.Matrix.MessageTemplates = "messages"
	}

	for name, target := range c.Targets {
		target.Type = strings.ToLower(strings.TrimSpace(target.Type))
		target.BaseURL = strings.TrimRight(strings.TrimSpace(target.BaseURL), "/")
		target.Visibility = strings.ToLower(strings.TrimSpace(target.Visibility))
		if target.Visibility == "" {
			target.Visibility = "private"
		}
		if target.SSHUser == "" {
			target.SSHUser = "git"
		}
		transport := strings.ToLower(strings.TrimSpace(target.MirrorTransport))
		switch transport {
		case "", "ssh":
			target.MirrorTransport = "ssh"
		case "https", "http":
			target.MirrorTransport = "https"
		default:
			// normalize unknown value to ssh; validation rejects it.
			target.MirrorTransport = transport
		}
		switch target.Type {
		case "github":
			if target.BaseURL == "" {
				target.BaseURL = "https://api.github.com"
			}
			if target.SSHHost == "" {
				if u, err := url.Parse(target.BaseURL); err == nil && u.Hostname() != "" && !strings.EqualFold(u.Hostname(), "api.github.com") {
					target.SSHHost = u.Hostname()
				} else {
					target.SSHHost = "github.com"
				}
			}
		case "gitlab":
			if target.BaseURL == "" {
				target.BaseURL = "https://gitlab.com/api/v4"
			}
		}
		c.Targets[name] = target
	}
}

// Validate checks the configuration for obvious mistakes.
func (c *Config) Validate() error {
	var errs []error

	if c.Forgejo.BaseURL == "" {
		errs = append(errs, errors.New("forgejo.base_url is required"))
	}
	if c.Forgejo.Token == "" {
		errs = append(errs, errors.New("forgejo.token is required"))
	}
	if c.Matrix.Homeserver == "" {
		errs = append(errs, errors.New("matrix.homeserver is required"))
	}
	if c.Matrix.AccessToken == "" {
		errs = append(errs, errors.New("matrix.access_token is required"))
	}
	if len(c.Matrix.InviteUsers) == 0 {
		errs = append(errs, errors.New("matrix.invite_users must contain at least one user"))
	}
	for i, u := range c.Matrix.InviteUsers {
		if !strings.HasPrefix(u, "@") || !strings.Contains(u, ":") {
			errs = append(errs, fmt.Errorf("matrix.invite_users[%d] %q is not a Matrix user ID", i, u))
		}
	}
	if c.SyncInterval.Std() < 10*time.Second {
		errs = append(errs, errors.New("sync_interval must be at least 10s"))
	}
	if interval, err := time.ParseDuration(c.MirrorInterval); err != nil {
		errs = append(errs, fmt.Errorf("mirror_interval: %w", err))
	} else if interval < 10*time.Minute {
		errs = append(errs, errors.New("mirror_interval must be at least 10m (Forgejo minimum)"))
	}

	for name, target := range c.Targets {
		switch target.Type {
		case "forgejo", "github", "gitlab":
		default:
			errs = append(errs, fmt.Errorf("targets.%s.type must be one of forgejo, github, gitlab", name))
		}
		if target.BaseURL == "" {
			errs = append(errs, fmt.Errorf("targets.%s.base_url is required", name))
		}
		if target.Token == "" {
			errs = append(errs, fmt.Errorf("targets.%s.token is required", name))
		}
		if target.Visibility != "private" && target.Visibility != "public" {
			errs = append(errs, fmt.Errorf("targets.%s.visibility must be private or public", name))
		}
		if target.MirrorTransport != "ssh" && target.MirrorTransport != "https" && target.MirrorTransport != "http" {
			errs = append(errs, fmt.Errorf("targets.%s.mirror_transport must be ssh or https", name))
		}
	}

	for _, name := range c.DefaultTargets {
		if _, ok := c.Targets[name]; !ok {
			errs = append(errs, fmt.Errorf("default_targets references unknown target %q", name))
		}
	}

	return errors.Join(errs...)
}

// RoomAliasFor returns the proposed alias for a repository room.
func (m MatrixConfig) RoomAliasFor(owner, repo, repoKey string) string {
	if !m.PublicRooms || strings.TrimSpace(m.RoomAliasTemplate) == "" {
		return ""
	}
	domain := homeserverDomain(m.Homeserver)
	alias := m.RoomAliasTemplate
	alias = strings.NewReplacer(
		"{owner}", strings.ReplaceAll(owner, "/", "_"),
		"{repo}", strings.ReplaceAll(repo, "/", "_"),
		"{repo_key}", strings.ReplaceAll(repoKey, "/", "_"),
		"{domain}", domain,
	).Replace(alias)
	return strings.TrimSpace(alias)
}

// RoomLinkFor renders the public room link.
func (m MatrixConfig) RoomLinkFor(alias, roomID, repoKey string) string {
	candidate := alias
	if strings.TrimSpace(candidate) == "" {
		candidate = roomID
	}
	return strings.NewReplacer(
		"{alias}", candidate,
		"{room_id}", roomID,
		"{repo}", repoKey,
	).Replace(m.RoomLinkTemplate)
}

func (c *Config) ShortlinkTemplateFile() string {
	if strings.TrimSpace(c.ShortlinkTemplatePath) != "" {
		return c.ShortlinkTemplatePath
	}
	return c.ShortlinkTemplate
}
func homeserverDomain(homeserver string) string {
	u, err := url.Parse(strings.TrimSpace(homeserver))
	if err != nil || u.Hostname() == "" {
		return "matrix.org"
	}
	return u.Hostname()
}
