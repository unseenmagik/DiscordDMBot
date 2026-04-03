# Docs

This folder holds the planning and operating notes for the Discord DM bot.

## Files

- `project-scope.md`: goals, constraints, and non-goals for the bot.

## Operational summary

- The bot reads runtime settings, schedules, and embed styling from `config/config.toml`.
- The bot token also lives in `config/config.toml`.
- Delivery history is stored in `data/delivery-state.json`.
- Runtime logs are written to `logs/` with daily rotation.
- The scheduler checks for due messages every `runtime.poll_interval_seconds`.
- Command usage is locked to `discord.guild_ids` and `discord.allowed_role_ids`.
- The bot can post admin status messages to `discord.admin_channel_id`.
- The running bot also posts an admin message when it detects and applies config changes from disk.
- `./bin/discord-dm-bot --check-config` validates the TOML file without starting the bot.
- Admin slash commands now include manual reminder resend, delivery ID listing, schedule removal, and saved-state clearing helpers.

## Recommended deployment pattern

- Build the Go binary on the server.
- Run it with PM2 and keep secrets outside the ecosystem file.
- Copy `config/config.toml.example` to `config/config.toml`.
- Keep `config/config.toml` in your deployment directory.
- Back up `data/delivery-state.json` if delivery history matters to you.
