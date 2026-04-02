# Project Scope

## Goal

Build a production-friendly Discord bot that sends formatted Discord DM reminder embeds to specific users, with delivery rules controlled from a local TOML config file and managed through restricted slash commands.

## In scope

- Payment reminder scheduling grouped by `[[deliveries]]`, with nested `[[deliveries.reminders]]` entries.
- Per-delivery reminder flows such as an initial reminder and a final reminder before the due date.
- Legacy one-off delivery support for compatibility while the project moves toward reminder-based scheduling.
- Admin slash commands for:
  - immediate test sends
  - adding new reminder schedules to config
  - viewing parsed config state as embeds
- Config-driven scheduling with:
  - Discord user ID
  - due date and optional due time
  - optional recurring frequency
    supports once, daily, weekly, bi-weekly, and monthly today
  - value/reference
  - per-reminder day offsets
  - per-reminder send times
  - per-reminder message content
- Guild lock to configured Discord guild IDs.
- Role lock so only configured roles can use slash commands.
- Shared embed styling with placeholder replacement.
- Optional catch-up behavior for missed reminders after downtime.
- Restart-safe delivery tracking to prevent duplicate sends.
- Deployment guidance for a single compiled binary under PM2.
- Basic project documentation and example config files.

## Out of scope

- Recurring schedules.
- Admin dashboard or web UI.
- Database-backed history.
- Multi-tenant bot management.
- Full in-Discord schedule editing and deletion workflows.
- Advanced retry queues or dead-letter processing.

## Technical decisions

- Language: Go
  - chosen for low runtime overhead, simple deployment, and strong fit for long-running services
- Discord integration: `discordgo`
  - used for slash commands, guild/member checks, and DM delivery
- Config storage: TOML file
  - stores Discord settings, runtime settings, embed settings, and delivery definitions in one readable config document
- Config layout:
  - committed example file at `config/config.toml.example`
  - real local file at `config/config.toml`, kept out of git
- State storage: JSON file on disk
  - enough for a single-server deployment
- Schedule expansion model:
  - reminder-based deliveries are expanded into concrete scheduled sends at runtime using the configured timezone

## Success criteria

- A server operator can copy `config/config.toml.example` to `config/config.toml`, fill in Discord credentials and guild/role restrictions, and define reminder schedules without code changes.
- A delivery group can define one payment due date and multiple reminders, each with its own message and send time.
- Slash commands only work inside configured guilds and only for members with configured allowed roles.
- The bot only sends to users that it can confirm are members of at least one configured guild.
- The scheduler can either skip or catch up missed reminders after downtime based on config.
- The bot can be restarted without resending completed reminder sends.
- The PM2 ecosystem file is used only to start the compiled binary, not to hold runtime settings.
- The process runs cleanly under PM2 as a single long-running service.
