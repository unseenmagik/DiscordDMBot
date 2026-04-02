package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"discorddmbot/internal/commands"
	"discorddmbot/internal/config"
	"discorddmbot/internal/delivery"

	"github.com/bwmarrin/discordgo"
)

func main() {
	logger := log.New(os.Stdout, "", log.Ldate|log.Ltime|log.LUTC)

	configPath := "config/config.toml"
	configStore := config.NewStore(configPath)
	appConfig, err := configStore.Load()
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}

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

	commandService := commands.NewService(session, configStore, logger, appConfig.Discord)
	if err := commandService.Register(botUser.ID); err != nil {
		logger.Fatalf("register slash commands: %v", err)
	}

	if err := session.Open(); err != nil {
		logger.Fatalf("open discord gateway session: %v", err)
	}

	logger.Printf(
		"discord dm bot is ready; config=%s state=%s guild_scope=%s",
		configPath,
		appConfig.Runtime.StatePath,
		strings.Join(appConfig.Discord.GuildIDs, ","),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	runner := delivery.NewRunner(session, configStore, appConfig.Runtime.StatePath, logger)
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
