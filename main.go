package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"discorddmbot/internal/commands"
	"discorddmbot/internal/config"
	"discorddmbot/internal/delivery"
	"discorddmbot/internal/logging"
	"discorddmbot/internal/state"

	"github.com/bwmarrin/discordgo"
)

func main() {
	flagSet := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	configPath := flagSet.String("config", "config/config.toml", "Path to the TOML config file.")
	checkConfig := flagSet.Bool("check-config", false, "Validate the config file and exit without starting the bot.")
	flagSet.Parse(os.Args[1:])

	logger := log.New(os.Stdout, "", log.Ldate|log.Ltime|log.LUTC)

	configStore := config.NewStore(*configPath)
	appConfig, err := configStore.Load()
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}

	location, err := time.LoadLocation(appConfig.Runtime.Timezone)
	if err != nil {
		logger.Fatalf("load runtime timezone: %v", err)
	}

	if *checkConfig {
		scheduledSends, err := countScheduledDeliveries(appConfig, location)
		if err != nil {
			logger.Fatalf("check config: %v", err)
		}
		fmt.Fprintf(
			os.Stdout,
			"config ok: path=%s timezone=%s guild_scope=%s deliveries=%d scheduled_sends=%d state=%s\n",
			*configPath,
			appConfig.Runtime.Timezone,
			strings.Join(appConfig.Discord.GuildIDs, ","),
			len(appConfig.Deliveries),
			scheduledSends,
			appConfig.Runtime.StatePath,
		)
		return
	}

	logger, logCloser, err := logging.NewLogger("logs", "discord-dm-bot", location)
	if err != nil {
		logger.Fatalf("create logger: %v", err)
	}
	defer logCloser.Close()

	session, err := discordgo.New("Bot " + appConfig.Discord.BotToken)
	if err != nil {
		logger.Fatalf("create discord session: %v", err)
	}
	defer session.Close()

	session.ShouldReconnectOnError = false
	session.Identify.Intents = discordgo.IntentsGuilds

	botUser, err := session.User("@me")
	if err != nil {
		logger.Fatalf("validate bot token: %v", err)
	}

	stateStore := state.NewStore(appConfig.Runtime.StatePath)
	commandService := commands.NewService(session, configStore, stateStore, logger, appConfig.Discord)
	if err := commandService.Register(botUser.ID); err != nil {
		logger.Fatalf("register slash commands: %v", err)
	}

	if err := session.Open(); err != nil {
		logger.Fatalf("open discord gateway session: %v", err)
	}

	logger.Printf(
		"discord dm bot is ready; config=%s state=%s guild_scope=%s",
		*configPath,
		appConfig.Runtime.StatePath,
		strings.Join(appConfig.Discord.GuildIDs, ","),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	runner := delivery.NewRunner(session, configStore, stateStore, logger)
	errCh := make(chan error, 1)
	go func() {
		errCh <- runner.Run(ctx)
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Fatalf("runner stopped with error: %v", err)
		}
	}
}

func countScheduledDeliveries(appConfig *config.Config, location *time.Location) (int, error) {
	now := time.Now().In(location)
	total := 0
	for _, deliveryConfig := range appConfig.Deliveries {
		scheduledDeliveries, err := deliveryConfig.ExpandAt(location, now)
		if err != nil {
			return 0, err
		}
		total += len(scheduledDeliveries)
	}

	return total, nil
}
