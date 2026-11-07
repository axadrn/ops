package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/axadrn/moin/controlplane/internal/access"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/google/uuid"
)

type SubscriptionService struct {
	repo          *repository.Subscriptions
	adminActivity *repository.Admin
	// activity, when set, gets plan and status changes of a workspace.
	activity *repository.Activity
}

// UseActivity writes plan and status changes to the activity of the
// workspace.
func (s *SubscriptionService) UseActivity(activity *repository.Activity) {
	s.activity = activity
}

// recordPlanChange writes a change of plan or status, from the payment
// provider, to the activity of the workspace.
func (s *SubscriptionService) recordPlanChange(before *model.Subscription, after *model.Subscription) {
	if s.activity == nil || before == nil || (before.PlanID == after.PlanID && before.Status == after.Status) {
		return
	}
	recordActivity(s.activity, time.Now(), System(after.WorkspaceID), "billing.plan_changed", "subscription", after.WorkspaceID, map[string]string{
		"from": before.PlanID, "to": after.PlanID, "status_from": before.Status, "status": after.Status, "provider": after.Provider,
	})
}

// current is the stored subscription of a workspace, nil when there is none.
func (s *SubscriptionService) current(workspaceID string) *model.Subscription {
	sub, err := s.repo.ByWorkspaceID(workspaceID)
	if err != nil {
		return nil
	}
	return sub
}

func NewSubscriptionService(repo *repository.Subscriptions, adminActivity *repository.Admin) *SubscriptionService {
	return &SubscriptionService{repo: repo, adminActivity: adminActivity}
}

func (s *SubscriptionService) CreateFreeSubscription(workspaceID string) error {
	now := time.Now()
	subscription := &model.Subscription{
		ID:          uuid.New().String(),
		WorkspaceID: workspaceID,
		PlanID:      model.SubscriptionPlanFree,
		Status:      model.SubscriptionStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	err := s.repo.Create(subscription)
	if err != nil {
		return fmt.Errorf("failed to create free subscription: %w", err)
	}

	return nil
}

func (s *SubscriptionService) Subscription(workspaceID string) (*model.Subscription, error) {
	sub, err := s.repo.ByWorkspaceID(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get subscription: %w", err)
	}

	return sub, nil
}

func (s *SubscriptionService) Access(workspaceID string) (access.Access, error) {
	subscription, err := s.Subscription(workspaceID)
	if err != nil {
		return access.Access{}, err
	}
	return subscription.Access(), nil
}

// EnsureSubscription restores the required row when an workspace is missing one.
func (s *SubscriptionService) EnsureSubscription(workspaceID string) (*model.Subscription, error) {
	sub, err := s.repo.ByWorkspaceID(workspaceID)
	if err == nil {
		return sub, nil
	}
	if !errors.Is(err, repository.ErrSubscriptionNotFound) {
		return nil, fmt.Errorf("failed to get subscription: %w", err)
	}

	// A restored row is free, never a new trial: the trial came with the
	// workspace.
	now := time.Now()
	subscription := &model.Subscription{
		ID:          uuid.New().String(),
		WorkspaceID: workspaceID,
		PlanID:      access.PlanFree,
		Status:      access.StatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.repo.CreateIfMissing(subscription); err != nil {
		return nil, fmt.Errorf("failed to restore subscription: %w", err)
	}
	sub, err = s.repo.ByWorkspaceID(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get restored subscription: %w", err)
	}
	return sub, nil
}

func (s *SubscriptionService) ByProviderSubscriptionID(providerSubID string) (*model.Subscription, error) {
	sub, err := s.repo.ByProviderSubscriptionID(providerSubID)
	if err != nil {
		return nil, fmt.Errorf("failed to get subscription by provider ID: %w", err)
	}

	return sub, nil
}

func (s *SubscriptionService) ByProviderCustomerID(providerCustomerID string) (*model.Subscription, error) {
	sub, err := s.repo.ByProviderCustomerID(providerCustomerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get subscription by provider customer ID: %w", err)
	}

	return sub, nil
}

func (s *SubscriptionService) UpdateSubscription(sub *model.Subscription, providerEventID ...string) error {
	sub.UpdatedAt = time.Now()
	before := s.current(sub.WorkspaceID)

	err := s.repo.Update(sub)
	if err != nil {
		return fmt.Errorf("failed to update subscription: %w", err)
	}
	s.recordPlanChange(before, sub)
	eventID := ""
	if len(providerEventID) > 0 {
		eventID = providerEventID[0]
	}
	if err := s.recordSubscriptionUpdate(sub, eventID); err != nil {
		return fmt.Errorf("record subscription activity event: %w", err)
	}

	return nil
}

func (s *SubscriptionService) BeginCheckout(workspaceID, provider, planID, interval string, expiresAt, pendingUntil time.Time) (string, string, bool, error) {
	attemptID := uuid.New().String()
	checkoutURL, reused, err := s.repo.BeginCheckout(workspaceID, attemptID, provider, planID, interval, expiresAt, pendingUntil, time.Now())
	if err != nil {
		return "", "", false, fmt.Errorf("failed to begin checkout: %w", err)
	}
	if reused && checkoutURL == "" {
		return "", "", false, fmt.Errorf("failed to begin checkout: reusable checkout has no URL")
	}
	return checkoutURL, attemptID, reused, nil
}

func (s *SubscriptionService) FinishCheckout(workspaceID, attemptID, checkoutURL string, expiresAt, pendingUntil time.Time) error {
	if err := s.repo.FinishCheckout(workspaceID, attemptID, checkoutURL, expiresAt, pendingUntil, time.Now()); err != nil {
		return fmt.Errorf("failed to finish checkout: %w", err)
	}
	return nil
}

func (s *SubscriptionService) RejectCheckout(workspaceID, attemptID string) {
	_ = s.repo.RejectCheckout(workspaceID, attemptID, time.Now())
}

func (s *SubscriptionService) UpdateProviderState(
	sub *model.Subscription,
	providerSubscriptionID string,
	stateUpdatedAt time.Time,
	subscriptionCreatedAt *time.Time,
) (bool, error) {
	sub.UpdatedAt = time.Now()
	before := s.current(sub.WorkspaceID)
	updated, err := s.repo.UpdateIfProviderStateNewer(sub, providerSubscriptionID, stateUpdatedAt, subscriptionCreatedAt)
	if err != nil {
		return false, fmt.Errorf("failed to update provider subscription state: %w", err)
	}
	if updated {
		s.recordPlanChange(before, sub)
	}
	activitySubscription := sub
	if !updated {
		activitySubscription, err = s.repo.ByWorkspaceID(sub.WorkspaceID)
		if err != nil {
			return false, fmt.Errorf("load current subscription for activity: %w", err)
		}
	}
	if err := s.recordSubscriptionUpdate(activitySubscription, ""); err != nil {
		return updated, fmt.Errorf("record subscription activity event: %w", err)
	}
	return updated, nil
}

func (s *SubscriptionService) UpdateProviderPayment(sub *model.Subscription, providerSubscriptionID string, paymentUpdatedAt time.Time) (bool, error) {
	sub.UpdatedAt = time.Now()
	updated, err := s.repo.UpdateIfProviderPaymentNewer(sub, providerSubscriptionID, paymentUpdatedAt)
	if err != nil {
		return false, fmt.Errorf("failed to update provider payment state: %w", err)
	}
	return updated, nil
}

func (s *SubscriptionService) UpdateProviderOrder(sub *model.Subscription, orderUpdatedAt time.Time) (bool, error) {
	sub.UpdatedAt = time.Now()
	updated, err := s.repo.UpdateIfProviderOrderNewer(sub, orderUpdatedAt)
	if err != nil {
		return false, fmt.Errorf("failed to update provider order state: %w", err)
	}
	return updated, nil
}

func (s *SubscriptionService) EndPaidAccess(sub *model.Subscription, providerEventID ...string) error {
	sub.Status = model.SubscriptionStatusInactive
	sub.ProviderSubscriptionID = nil
	sub.CurrentPeriodEnd = nil
	sub.Amount = nil
	sub.Currency = ""
	sub.Interval = nil
	sub.ClearCheckout()

	return s.UpdateSubscription(sub, providerEventID...)
}

func (s *SubscriptionService) recordSubscriptionUpdate(sub *model.Subscription, providerEventID string) error {
	metadata, err := json.Marshal(map[string]string{
		"plan_id":  sub.PlanID,
		"provider": sub.Provider,
		"status":   sub.Status,
	})
	if err != nil {
		return err
	}
	providerSubscriptionID := ""
	if sub.ProviderSubscriptionID != nil {
		providerSubscriptionID = *sub.ProviderSubscriptionID
	}
	periodEnd := ""
	if sub.CurrentPeriodEnd != nil {
		periodEnd = sub.CurrentPeriodEnd.UTC().Format(time.RFC3339Nano)
	}
	stateUpdatedAt := ""
	if sub.ProviderStateUpdatedAt != nil {
		stateUpdatedAt = sub.ProviderStateUpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	key := ""
	if providerEventID != "" {
		key = strings.Join([]string{"provider-event", sub.Provider, providerEventID}, "|")
	} else {
		key = strings.Join([]string{
			sub.WorkspaceID,
			sub.Provider,
			providerSubscriptionID,
			sub.PlanID,
			sub.Status,
			periodEnd,
			stateUpdatedAt,
		}, "|")
	}
	workspaceID := sub.WorkspaceID
	event := &model.AdminActivityEvent{
		ID:          uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String(),
		WorkspaceID: &workspaceID,
		Action:      "subscription.updated",
		TargetType:  "subscription",
		TargetID:    sub.ID,
		Metadata:    string(metadata),
		CreatedAt:   time.Now(),
	}
	return s.adminActivity.CreateActivityEvent(event)
}
