package telegrambot

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const maxWebhookBodyBytes int64 = 256 << 10

type UpdateRepository interface {
	RecordTelegramUpdate(context.Context, int64, int64) (bool, error)
}

type Sender interface {
	SendMessage(context.Context, int64, string) error
	SendWebAppButton(context.Context, int64, string, string, string) error
}

type Replier interface {
	Reply(context.Context, int64, string) (Reply, error)
}

type WebhookDependencies struct {
	Updates  UpdateRepository
	Sender   Sender
	Replier  Replier
	Secret   string
	AdminIDs []int64
	Now      func() time.Time
	Logger   *slog.Logger
}

type webhookHandler struct {
	updates    UpdateRepository
	sender     Sender
	replier    Replier
	wantSecret [sha256.Size]byte
	adminIDs   map[int64]struct{}
	now        func() time.Time
	logger     *slog.Logger
}

type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message"`
}

type telegramMessage struct {
	From *telegramUser `json:"from"`
	Chat *telegramChat `json:"chat"`
	Text string        `json:"text"`
}

type telegramUser struct {
	ID int64 `json:"id"`
}

type telegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

func NewWebhookHandler(dependencies WebhookDependencies) (http.Handler, error) {
	if dependencies.Updates == nil || dependencies.Sender == nil || dependencies.Replier == nil {
		return nil, errors.New("create Telegram webhook handler: dependencies are required")
	}
	if strings.TrimSpace(dependencies.Secret) == "" {
		return nil, errors.New("create Telegram webhook handler: secret is required")
	}
	if len(dependencies.AdminIDs) == 0 {
		return nil, errors.New("create Telegram webhook handler: administrator IDs are required")
	}
	adminIDs := make(map[int64]struct{}, len(dependencies.AdminIDs))
	for _, adminID := range dependencies.AdminIDs {
		if adminID <= 0 {
			return nil, errors.New("create Telegram webhook handler: invalid administrator ID")
		}
		if _, exists := adminIDs[adminID]; exists {
			return nil, errors.New("create Telegram webhook handler: duplicate administrator ID")
		}
		adminIDs[adminID] = struct{}{}
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	if dependencies.Logger == nil {
		dependencies.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	return &webhookHandler{
		updates:    dependencies.Updates,
		sender:     dependencies.Sender,
		replier:    dependencies.Replier,
		wantSecret: sha256.Sum256([]byte(dependencies.Secret)),
		adminIDs:   adminIDs,
		now:        dependencies.Now,
		logger:     dependencies.Logger,
	}, nil
}

func (handler *webhookHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	gotSecret := sha256.Sum256([]byte(request.Header.Get("X-Telegram-Bot-Api-Secret-Token")))
	if subtle.ConstantTimeCompare(gotSecret[:], handler.wantSecret[:]) != 1 {
		http.NotFound(writer, request)
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, maxWebhookBodyBytes)
	decoder := json.NewDecoder(request.Body)
	var update telegramUpdate
	if err := decoder.Decode(&update); err != nil {
		writeWebhookDecodeError(writer, err)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeWebhookDecodeError(writer, err)
		return
	}
	if update.UpdateID <= 0 {
		http.Error(writer, "invalid update", http.StatusBadRequest)
		return
	}
	inserted, err := handler.updates.RecordTelegramUpdate(request.Context(), update.UpdateID, handler.now().UnixMilli())
	if err != nil {
		http.Error(writer, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	if !inserted {
		writer.WriteHeader(http.StatusNoContent)
		return
	}

	message := update.Message
	if message == nil || message.From == nil || message.Chat == nil || message.Chat.Type != "private" || message.Text == "" {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if _, trusted := handler.adminIDs[message.From.ID]; !trusted {
		writer.WriteHeader(http.StatusNoContent)
		return
	}

	reply, err := handler.replier.Reply(request.Context(), message.From.ID, message.Text)
	if err != nil {
		handler.logCommandFailure("reply_failed", update.UpdateID, message.From.ID)
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	var sendErr error
	if reply.WebAppURL == "" {
		sendErr = handler.sender.SendMessage(request.Context(), message.Chat.ID, reply.Text)
	} else {
		sendErr = handler.sender.SendWebAppButton(request.Context(), message.Chat.ID, reply.Text, reply.ButtonText, reply.WebAppURL)
	}
	if sendErr != nil {
		handler.logCommandFailure("send_message_failed", update.UpdateID, message.From.ID)
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *webhookHandler) logCommandFailure(operation string, updateID, adminID int64) {
	handler.logger.Error(
		"telegram webhook command failed",
		"operation", operation,
		"update_id", updateID,
		"admin_id", adminID,
	)
}

func writeWebhookDecodeError(writer http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		http.Error(writer, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(writer, "invalid JSON", http.StatusBadRequest)
}
