package service

import (
	"context"
	"errors"
	"testing"

	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type fakeDeviceStore struct {
	devices []model.UserDevice
	created bool
	err     error
}

func (store *fakeDeviceStore) Register(
	_ context.Context,
	_ database.Tx,
	userID uuid.UUID,
	device model.UserDevice,
) (model.UserDevice, bool, error) {
	device.UserID = userID
	store.devices = append(store.devices, device)
	return device, store.created, store.err
}

func (store *fakeDeviceStore) List(
	context.Context,
	database.Tx,
	uuid.UUID,
) ([]model.UserDevice, error) {
	return append([]model.UserDevice(nil), store.devices...), store.err
}

func TestDeviceServiceRegistersClientGeneratedDeviceAndAuditsBootstrapWrite(t *testing.T) {
	store := &fakeDeviceStore{created: true}
	audits := &recordingAuditStore{}
	auditService, _ := NewAuditService(audits)
	service, err := NewDeviceService(store, immediateUserTransactor{tx: &testTransaction{}}, auditService)
	if err != nil {
		t.Fatal(err)
	}
	userID := uuid.New()
	deviceID := uuid.New()
	registration, err := service.Register(context.Background(), userID, deviceID, RegisterDeviceInput{
		DeviceName: "  Chrome on Laptop  ", Platform: "web",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !registration.Created || registration.Device.ID != deviceID || registration.Device.UserID != userID ||
		registration.Device.DeviceName != "Chrome on Laptop" {
		t.Fatalf("registration = %#v", registration)
	}
	if len(audits.drafts) != 1 || audits.drafts[0].Action != "device.register" || audits.drafts[0].ActorType != "user" {
		t.Fatalf("audits = %#v", audits.drafts)
	}
}

func TestDeviceServiceValidatesRegistrationAndListsDevices(t *testing.T) {
	store := &fakeDeviceStore{}
	auditService, _ := NewAuditService(&recordingAuditStore{})
	service, _ := NewDeviceService(store, immediateUserTransactor{tx: &testTransaction{}}, auditService)
	for _, input := range []RegisterDeviceInput{
		{DeviceName: "", Platform: "web"},
		{DeviceName: "Browser", Platform: "unknown"},
	} {
		if _, err := service.Register(context.Background(), uuid.New(), uuid.New(), input); !errors.Is(err, ErrValidation) {
			t.Fatalf("Register(%#v) error = %v", input, err)
		}
	}
	store.devices = []model.UserDevice{{ID: uuid.New(), DeviceName: "Browser", Platform: "web"}}
	devices, err := service.List(context.Background(), uuid.New())
	if err != nil || len(devices) != 1 {
		t.Fatalf("List() = %#v, %v", devices, err)
	}
}
