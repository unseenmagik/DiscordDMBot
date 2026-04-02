# Discord DM Bot

A small Go-based Discord bot that sends one-off direct messages to specific users at specific dates and times as formatted Discord embeds, with admin slash commands for sending and managing schedules.

## Why this approach

This project uses Go instead of Node for the runtime because it is lightweight, fast, easy to deploy as a single binary, and a good fit for a long-running scheduler process. It still works well with PM2, but avoids a larger runtime and dependency surface.

Security choices in this scaffold:

- The real `config/config.toml` file is gitignored, so your committed repo only contains `config/config.toml.example`.
- The bot only uses the `Guilds` gateway intent needed for slash commands.
- Sent-message state is written to `data/delivery-state.json` so the same job is not sent twice after a restart.
- The config file is validated before each processing pass.
- The bot only accepts commands in the configured `discord.guild_ids`.
- Slash commands only work for members who have one of the configured `discord.allowed_role_ids`.
- The bot verifies that target users belong to at least one configured guild before sending or scheduling.

## Project layout

- `main.go`: bootstraps the bot and process lifecycle.
- `internal/config`: TOML config loading and validation.
- `internal/delivery`: scheduler loop and DM sending.
- `internal/state`: restart-safe delivery tracking.
- `config/config.toml.example`: committed example config.
- `internal/commands`: slash command registration and handlers.
- `docs/`: project scope and documentation.

## Config format

Copy `config/config.toml.example` to `config/config.toml`, then edit `config/config.toml`:

```toml
[discord]
bot_token = "replace-with-your-bot-token"
guild_ids = ["123456789012345678"]
allowed_role_ids = ["345678901234567890"]

[runtime]
timezone = "Europe/London"
poll_interval_seconds = 15
state_path = "data/delivery-state.json"

[embed]
title = "Scheduled Reminder"
description_template = "Hello, this is your scheduled message.\n\nYour value is: **{{value}}**"
footer = "Sent automatically by the Discord DM Bot"
color = "#2B6CB0"

[[deliveries]]
id = "invoice-reminder-001"
user_id = "123456789012345678"
date = "2026-04-15"
time = "09:30"
value = "INV-2026-001"
```

Supported placeholders in `embed.description_template` and per-delivery `message`:

- `{{value}}`
- `{{userId}}`
- `{{date}}`
- `{{time}}`

Each DM is sent as an embed with:

- a shared title
- a shared footer
- a configurable accent color
- a description built from the template
- `Value` and `Scheduled For` fields added automatically

You can also set a per-delivery `message` field if you need a one-off description override.

## Run locally

1. Create your real config:

```bash
cp config/config.toml.example config/config.toml
```

Then edit:

- `discord.bot_token` to your real Discord bot token
- `discord.guild_ids` to your Discord server ID list
- `discord.allowed_role_ids` to the role ID or IDs allowed to use the bot
- your delivery entries

If the slash commands do not appear in Discord, make sure the bot was invited with the `applications.commands` scope as well as the bot scope.

2. Download dependencies:

```bash
go mod tidy
```

3. Build:

```bash
mkdir -p bin
go build -o bin/discord-dm-bot .
```

4. Start:

```bash
./bin/discord-dm-bot
```

## Slash commands

After the bot starts, it registers these admin commands:

- `/send-now user value [message]`
- `/schedule-add user date time value [id] [message]`
- `/schedule-view`

Use `/send-now` for an immediate DM and `/schedule-add` for a DM at a chosen future date and time.

Command formats:

- `date` uses `YYYY-MM-DD`
- `time` uses 24-hour `HH:MM`
- `message` is an optional custom embed description override

`/schedule-view` reads the TOML config and returns parsed embed pages instead of raw TOML.

## Discord Details

Your Discord bot details all go in one place:

- `config/config.toml`
  This includes `discord.bot_token`, `discord.guild_ids`, and `discord.allowed_role_ids`.

Your real `config/config.toml` should stay out of git. Only `config/config.toml.example` is meant to be committed.

## Run with PM2

1. Build the binary as shown above.
2. Copy `ecosystem.config.example.cjs` to your own PM2 config file.
3. Make sure `config/config.toml` exists on the server.
4. Start it:

```bash
pm2 start ecosystem.config.example.cjs
```

## Important Discord note

Bots can only DM users when Discord allows it. In practice, the user usually needs to share a server with the bot and permit DMs from server members, or already have an open DM relationship with the bot.

## Docs

- `docs/readme.md`
- `docs/project-scope.md`
