# Discord DM Bot

A Go-based Discord bot for sending scheduled DM embeds to specific users, with slash commands for immediate sends, adding schedules, and viewing config state.

## What It Does

- Sends scheduled Discord DMs as embeds
- Stores schedules in `config/config.toml`
- Restricts bot usage to configured guilds and roles
- Exposes admin slash commands for managing sends
- Runs cleanly as a single compiled binary under PM2

## Requirements

- Go installed on the server
- PM2 installed on the server
- A Discord bot token
- The bot invited to your server with:
  - `bot`
  - `applications.commands`

## Server Setup

On your server, inside the project directory:

```bash
cp config/config.toml.example config/config.toml
```

Then edit `config/config.toml`.

## Config

Example:

```toml
[discord]
bot_token = "replace-with-your-bot-token"
guild_ids = ["123456789012345678"]
allowed_role_ids = ["345678901234567890"]

[runtime]
timezone = "Europe/London"
poll_interval_seconds = 15
send_missed_deliveries = false
state_path = "data/delivery-state.json"

[embed]
title = "Scheduled Reminder"
description_template = "Hello,\n\nThis is your payment reminder for **{{value}}**."
footer = "Sent automatically by the Discord DM Bot"
color = "#2B6CB0"

[[deliveries]]
id = "payment-reminder-001"
user_id = "123456789012345678"
due_date = "2026-04-15"
due_time = "17:00"
value = "INV-2026-001"

[[deliveries.reminders]]
id = "initial"
name = "Initial Reminder"
days_before_due = 3
time = "09:00"
message = "Hello,\n\nThis is your **initial reminder** that payment **{{value}}** is due on **{{dueDate}}**."

[[deliveries.reminders]]
id = "final"
name = "Final Reminder"
days_before_due = 1
time = "09:00"
message = "Hello,\n\nThis is your **final reminder** that payment **{{value}}** is due on **{{dueDate}}**."
```

Set these first:

- `discord.bot_token`
  Your Discord bot token
- `discord.guild_ids`
  The guild IDs where the bot is allowed to operate
- `discord.allowed_role_ids`
  The role IDs allowed to use the slash commands
- `runtime.timezone`
  Timezone used for scheduled sends
- `runtime.poll_interval_seconds`
  How often the bot checks for due deliveries
- `runtime.send_missed_deliveries`
  If `true`, the bot will send old missed jobs after downtime; if `false`, it only sends inside the live schedule window

For due-date reminder flows, each `[[deliveries]]` can contain:

- `due_date`
- optional `due_time`
- one or more `[[deliveries.reminders]]`

Each `[[deliveries.reminders]]` entry belongs to the `[[deliveries]]` block directly above it and uses that parent delivery's `user_id`, `value`, `due_date`, and `due_time`.

For a simple one-off send, you can still use:

- `date`
- `time`
- optional `message`

## Delivery Template

Supported placeholders in `embed.description_template` and per-delivery `message`:

- `{{value}}`
- `{{userId}}`
- `{{date}}`
- `{{time}}`
- `{{dueDate}}`
- `{{dueTime}}`
- `{{reminderName}}`
- `{{daysBeforeDue}}`

Each DM is sent as an embed with:

- the configured embed title
- the configured footer
- the configured color
- the rendered description
- automatic `Value` and `Scheduled For` fields

## Build

Run:

```bash
go mod tidy
mkdir -p bin
go build -o bin/discord-dm-bot .
```

## Run Manually

For a quick test:

```bash
./bin/discord-dm-bot
```

If startup is correct, the bot will validate the token, register slash commands in the configured guilds, and begin polling for scheduled deliveries.

## PM2 Setup

Copy the example file if needed:

```bash
cp ecosystem.config.example.cjs ecosystem.config.cjs
```

Then edit `ecosystem.config.cjs` and set the real project path in `cwd`.

Example:

```js
module.exports = {
  apps: [
    {
      name: "discord-dm-bot",
      script: "./bin/discord-dm-bot",
      cwd: "/home/your-user/DiscordDMBot"
    }
  ]
};
```

## Start With PM2

From the project directory:

```bash
pm2 start ecosystem.config.cjs
```

Useful PM2 commands:

```bash
pm2 status
pm2 logs discord-dm-bot
pm2 restart discord-dm-bot
pm2 stop discord-dm-bot
pm2 delete discord-dm-bot
```

To make PM2 survive reboots:

```bash
pm2 save
pm2 startup
```

Run the command printed by `pm2 startup`, then run `pm2 save` again if needed.

## Updating The Bot

After pulling new changes on the server:

```bash
go mod tidy
go build -o bin/discord-dm-bot .
pm2 restart discord-dm-bot
```

## Slash Commands

The bot registers these slash commands in the configured guilds:

- `/send-now user value [message]`
- `/schedule-add user due_date value initial_time initial_message final_time final_message [due_time] [initial_days_before] [final_days_before] [id]`
- `/schedule-view`

`/schedule-add` creates a payment schedule with two reminders:

- `initial`
- `final`

Formats:

- `due_date`: `YYYY-MM-DD`
- `due_time`: optional `HH:MM` using 24-hour time
- `initial_time`: `HH:MM`
- `final_time`: `HH:MM`
- `initial_days_before`: optional, defaults to `3`
- `final_days_before`: optional, defaults to `1`

## Security Notes

- Your real `config/config.toml` is gitignored
- Only `config/config.toml.example` should be committed
- The bot only accepts slash commands from configured guilds
- The bot only allows members with configured role IDs to use commands
- The bot only sends to users found in at least one configured guild

## Discord Notes

Bots can only DM users when Discord allows it. A user usually needs to:

- share a server with the bot
- allow DMs from server members, or
- already have an open DM relationship with the bot

## Docs

- `docs/readme.md`
- `docs/project-scope.md`
