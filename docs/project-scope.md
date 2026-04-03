# Project Scope

## Goal

Deliver a production-friendly Discord bot that sends scheduled payment reminder embeds by DM, is operated from a local TOML config file, and gives admins enough in-Discord tooling to manage schedules safely without needing a web dashboard.

## Current scope

- Reminder-based delivery groups stored under `[[deliveries]]` with nested `[[deliveries.reminders]]`.
- Reminder flows that support:
  - `initial`
  - `final`
  - `due`
  - `late` as a manual-only reminder
- Legacy one-off delivery support using `date` and `time`.
- Recurring due-date schedules using:
  - `once`
  - `daily`
  - `weekly`
  - `bi-weekly`
  - `monthly`
- Shared embed presentation config in `[embed]`, with reminder-specific color overrides and placeholder replacement.
- Per-reminder title and message overrides.
- Slash-command administration for:
  - immediate test sends
  - manual reminder resend
  - schedule creation
  - schedule editing
  - schedule viewing
  - delivery ID listing
  - schedule removal
  - delivery-state clearing for testing and recovery
- Guild lock to configured `discord.guild_ids`.
- Role lock to configured `discord.allowed_role_ids`.
- Admin-channel monitoring through `discord.admin_channel_id`, including:
  - successful reminder sends
  - skipped missed-window notifications
  - failed send notifications
  - config-change applied notifications
  - due-day posts with a `Late reminder` button when a `late` reminder exists
- Restart-safe delivery tracking in `data/delivery-state.json`.
- Config hot reload during scheduler polling.
- Config validation mode using `--check-config`.
- Runtime logs with daily rotation under `logs/`.
- Single-binary deployment under PM2.
- Repository automation with a GitHub Actions workflow for Discord notifications using a GitHub secret-backed webhook.

## Operator workflow in scope

- Copy `config/config.toml.example` to `config/config.toml`.
- Configure bot token, guild restrictions, allowed roles, admin channel, embed defaults, and delivery groups.
- Validate config with `./bin/discord-dm-bot --check-config`.
- Run the compiled bot under PM2.
- Manage schedules from Discord using slash commands for add, edit, inspect, resend, remove, and state reset tasks.

## Technical decisions

- Language: Go
  - chosen for low runtime overhead, simple deployment, and strong fit for a long-running scheduler/service
- Discord integration: `discordgo`
  - used for slash commands, interaction handling, guild checks, admin-channel posts, and DM delivery
- Config storage: TOML
  - keeps operational settings and delivery definitions in one human-readable file
- Config file layout:
  - committed example file at `config/config.toml.example`
  - real local file at `config/config.toml`, excluded from git
- Delivery state storage: JSON on disk
  - sufficient for the current single-instance deployment model
- Scheduler model:
  - reminder-based schedules are expanded into concrete occurrences at runtime using the configured timezone
- Admin interaction model:
  - the bot posts its own monitoring messages in the admin channel rather than relying on Discord webhooks for bot operations

## Security and operational assumptions

- The bot only sends to users it can confirm are members of at least one configured guild.
- The bot does not expose an inbound HTTP server or arbitrary command execution surface.
- Secrets are expected to remain in the untracked local `config/config.toml`.
- The server is assumed to be a trusted host managed by the operator.
- PM2 is used only to supervise the compiled binary, not to store runtime config values.

## Explicitly out of scope

- A web dashboard or browser-based admin UI.
- Admin dashboard or web UI.
- Database-backed persistence.
- Database-backed history.
- Multi-tenant or multi-customer bot hosting.
- Multi-tenant bot management.
- Cross-server user discovery outside configured guild membership.
- Automatic overdue escalation beyond the configured reminder model and manual late-reminder action.
- Arbitrary custom recurrence rules beyond the supported frequency set.
- Full analytics, reporting, or payment reconciliation.
- High-availability distributed scheduling.
- Advanced retry queues or dead-letter processing.

## Success criteria

- A server operator can configure and run the bot without changing application code.
- Reminder groups can define multiple messages around one due date and optional recurrence.
- Admin access is limited to configured guilds and allowed roles.
- The bot can be restarted without resending completed reminders.
- Operators can test, resend, inspect, remove, and reset schedules from Discord itself.
- The admin channel provides enough visibility to monitor real sends, actionable failures, and applied config changes.
- The repository is safe to publish with only example config tracked and real secrets excluded.
