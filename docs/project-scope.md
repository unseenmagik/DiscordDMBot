# Project Scope

## Goal

Build a production-friendly Discord bot that sends direct messages to specific Discord users at scheduled dates and times, with delivery details controlled from a config file.

## In scope

- One-off scheduled DM delivery.
- Admin slash commands for immediate sending, config writes, and config viewing.
- Config-driven scheduling with:
  - Discord user ID
  - date
  - time
  - value
  - optional per-message override
- Guild lock to configured Discord server IDs.
- Role lock so only configured roles can use slash commands.
- Shared embed template with placeholder replacement.
- Secure secret handling through environment variables.
- Restart-safe delivery tracking to prevent duplicate sends.
- Deployment guidance for PM2.
- Basic project documentation.

## Out of scope

- Recurring schedules.
- Admin dashboard or web UI.
- Database-backed history.
- Multi-tenant bot management.
- Advanced retry queues or dead-letter processing.

## Technical decisions

- Language: Go
  - chosen for low runtime overhead, simple deployment, and strong fit for long-running services
- Discord integration: REST-based DM sending
  - avoids unnecessary gateway subscriptions and intents
- Config storage: TOML file
  - groups runtime settings and delivery entries in one readable config document
- State storage: JSON file on disk
  - enough for a single-server deployment

## Success criteria

- A server operator can edit one config file to schedule DMs.
- The bot can be restarted without resending completed deliveries.
- All bot settings, including Discord credentials and runtime settings, live in one local TOML file.
- Example config files can be committed without personal server details or secrets.
- The PM2 ecosystem file is used only to start the binary, not to hold runtime settings.
- The process runs cleanly under PM2.
