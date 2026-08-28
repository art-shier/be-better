package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type DeviceStore interface {
	Register(context.Context, database.Tx, uuid.UUID, model.UserDevice) (model.UserDevice, bool, error)
	List(context.Context, database.Tx, uuid.UUID) ([]model.UserDevice, error)
}

type DeviceService struct {
	store      DeviceStore
	transactor UserTransactor
	audit      *AuditService
}

type RegisterDeviceInput struct {
	DeviceName string `json:"deviceName"`
	Platform   string `json:"platform"`
}

type DeviceRegistration struct {
	Device  model.UserDevice `json:"device"`
	Created bool             `json:"-"`
}

func NewDeviceService(store DeviceStore, transactor UserTransactor, audit *AuditService) (*DeviceService, error) {
	if store == nil || transactor == nil || audit == nil {
		return nil, errors.New("device store, transactor, and audit service are required")
	}
	return &DeviceService{store: store, transactor: transactor, audit: audit}, nil
}

func (service *DeviceService) Register(
	ctx context.Context,
	userID uuid.UUID,
	deviceID uuid.UUID,
	input RegisterDeviceInput,
) (DeviceRegistration, error) {
	input.DeviceName = strings.TrimSpace(input.DeviceName)
	input.Platform = strings.ToLower(strings.TrimSpace(input.Platform))
	if userID == uuid.Nil || deviceID == uuid.Nil || utf8.RuneCountInString(input.DeviceName) < 1 ||
		utf8.RuneCountInString(input.DeviceName) > 120 || !validDevicePlatform(input.Platform) {
		return DeviceRegistration{}, fmt.Errorf("%w: invalid device registration", ErrValidation)
	}
	var registration DeviceRegistration
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		device, created, registerErr := service.store.Register(ctx, tx, userID, model.UserDevice{
			ID: deviceID, DeviceName: input.DeviceName, Platform: input.Platform,
		})
		if registerErr != nil {
			return registerErr
		}
		registration = DeviceRegistration{Device: device, Created: created}
		action := "device.refresh"
		if created {
			action = "device.register"
		}
		after, _ := json.Marshal(device)
		return service.audit.Record(ctx, tx, userID, []model.AuditDraft{{Action: action, AfterData: after}})
	})
	return registration, err
}

func (service *DeviceService) List(ctx context.Context, userID uuid.UUID) ([]model.UserDevice, error) {
	if userID == uuid.Nil {
		return nil, fmt.Errorf("%w: device user is required", ErrValidation)
	}
	var devices []model.UserDevice
	err := service.transactor.WithUser(ctx, userID, func(ctx context.Context, tx database.Tx) error {
		var readErr error
		devices, readErr = service.store.List(ctx, tx, userID)
		return readErr
	})
	return devices, err
}

func validDevicePlatform(platform string) bool {
	return map[string]bool{
		"web": true, "windows": true, "macos": true, "linux": true,
		"ios": true, "android": true,
	}[platform]
}
