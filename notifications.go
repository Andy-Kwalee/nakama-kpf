package kpf

import (
    "context"
    "database/sql"
    "encoding/json"

    "github.com/heroiclabs/hiro"
    "github.com/heroiclabs/nakama-common/runtime"
)

// You may need to move the PushClient struct and methods here as well, if they are not already in your module.
// For example:
// type PushClient struct { ... }
// func NewPushClientFromEnv(...) { ... }
// func (pc *PushClient) SendFirebasePush(...) { ... }

type NotificationRequest struct {
    UserID      string `json:"user_id"`
    DeviceToken string `json:"device_token"`
    Title       string `json:"title"`
    Body        string `json:"body"`
    ImageURL    string `json:"image_url"`
    ExternalID  string `json:"external_id"`
    IsIOS       bool   `json:"is_ios"`
}

func RpcSendNotification(pushClient *PushClient) func(context.Context, runtime.Logger, *sql.DB, runtime.NakamaModule, string) (string, error) {
    responseJSON := `{"message": "Push notification sent successfully"}`
    return func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
        var req NotificationRequest
        if err := json.Unmarshal([]byte(payload), &req); err != nil {
            logger.Debug("json.Unmarshal error: %v", err)
            return "", hiro.ErrBadInput
        }

        if !req.IsIOS {
            if err := pushClient.SendFirebasePush(ctx, req.DeviceToken, req.Title, req.Body, req.ImageURL, req.ExternalID); err != nil {
                return "", err
            }
        } else {
            if err := pushClient.SendApplePush(ctx, req.DeviceToken, req.Title, req.Body, req.ImageURL, req.ExternalID); err != nil {
                return "", err
            }
        }

        return responseJSON, nil
    }
}