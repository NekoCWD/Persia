# TODO

## Done

- [x] Discover repos through the `persia-managed` topic
- [x] Create a Matrix room per repo and invite the owner
- [x] Create repositories on Forgejo/GitHub/GitLab
- [x] Push mirrors over SSH plus automatic deploy key registration
- [x] Notifications for new/updated issues and pull requests
- [x] Notifications for push mirror errors and recovery
- [x] CI failure notifications (Forgejo Actions, GitHub Actions, GitLab)
- [x] SQLite state and duplicate notification protection
- [x] CLI: `run`, `once`, `doctor`, `version`
- [x] Dockerfile plus docker-compose draft
- [x] Owner scoping and HTTPS fallback for stubborn mirrors
- [x] Private management room for sensitive operations
- [x] Issue/PR notifications with description and links (local + remote)
- [x] Cleanup of state/mirrors/keys when a repo is no longer managed
- [x] Per-repository landing pages with mirror listing and public room link
- [x] Customizable landing-page template (`shortlink_template`, panic on missing/invalid)
- [x] Browsable repository links derived from the push mirror address (`repo_url_template`)
- [x] Repository description and avatar from the Forgejo API (in-memory cache)
- [x] Configurable Matrix notification templates (`matrix.message_templates`)

## Deployment

- [x] Add healthcheck to compose
- [x] Document SQLite backup (volume `persia-data`)
- [x] Add an example systemd unit for non-Docker use
- [ ] Verify and enable Docker Compose on the target server
- [ ] Configure a TLS reverse proxy for the shortlink domain

## Feature backlog

- [ ] Show new comment text instead of just the counter
- [ ] Notify when CI recovers after a failure
- [ ] Notifications for releases, tags and force-pushes
- [ ] Support several local Forgejo instances
- [ ] Aggregate multiple events into one message when busy
- [ ] Configure room permissions and aliases beyond private rooms
- [ ] Optional webhooks as the fast path in addition to polling
- [ ] Prometheus metrics and a richer `/healthz`
- [ ] Per-cycle repository caps and rate-limit backoff
- [ ] E2E test against a temporary Forgejo + Matrix instance in CI