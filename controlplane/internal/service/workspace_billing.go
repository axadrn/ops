package service

import (
	"errors"
	"log/slog"

	"github.com/axadrn/moin/controlplane/internal/access"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
)

var (
	// ErrBillingPermission is returned to members whose role may not
	// manage billing.
	ErrBillingPermission = errors.New("workspace billing permission required")
	ErrPlan              = errors.New("choose an available paid plan")
	ErrInterval          = errors.New("choose monthly or yearly billing")
	// ErrNoBillingAccount: the provider knows no customer for the
	// workspace yet, so there is no billing to manage.
	ErrNoBillingAccount = errors.New("start a paid plan before managing billing")
	// ErrCheckoutFailed and ErrBillingManagementFailed stand for an error
	// of the provider, which is logged.
	ErrCheckoutFailed          = errors.New("checkout could not be started")
	ErrBillingManagementFailed = errors.New("billing management could not be opened")
)

// ManageBilling is nil for a member who may manage the billing of the
// workspace, and ErrBillingPermission for everyone else.
func ManageBilling(member *model.WorkspaceMember) error {
	if member == nil || !member.Can(model.WorkspacePermissionManageBilling) {
		return ErrBillingPermission
	}
	return nil
}

// Checkout starts the checkout of a paid plan for the member's workspace
// with start, the CreateCheckoutURL of the payment provider, and returns
// its URL. interval is monthly, the default, or yearly. A checkout that is
// open already is a repository error such as ErrCheckoutPending.
func (s *WorkspaceService) Checkout(member *model.WorkspaceMember, workspace *model.Workspace, user *model.User, planID, interval string, source Source, start func(workspaceID, workspaceSlug, planID, interval, customerEmail, customerName string) (string, error)) (string, error) {
	if err := ManageBilling(member); err != nil {
		return "", err
	}
	plan, exists := access.PlanByID(planID)
	if !exists || !plan.Purchasable {
		return "", ErrPlan
	}
	if interval == "" {
		interval = model.SubscriptionIntervalMonthly
	}
	if interval != model.SubscriptionIntervalMonthly && interval != model.SubscriptionIntervalYearly {
		return "", ErrInterval
	}
	checkoutURL, err := start(workspace.ID, workspace.Slug, planID, interval, user.Email, workspace.Name)
	if errors.Is(err, repository.ErrCheckoutPending) || errors.Is(err, repository.ErrCheckoutSelection) || errors.Is(err, repository.ErrSubscriptionAlreadyPaid) {
		return "", err
	}
	if err != nil {
		slog.Error("failed to create checkout", "error", err, "workspace_id", workspace.ID, "plan_id", planID)
		return "", ErrCheckoutFailed
	}
	s.recordBilling(workspace.ID, user.ID, "billing.checkout_started", source)
	slog.Info("checkout created", "workspace_id", workspace.ID)
	return checkoutURL, nil
}

// BillingManagement returns the URL of the billing management of the
// payment provider for the member's workspace, made by open, the
// provider's BillingManagementURL.
func (s *WorkspaceService) BillingManagement(member *model.WorkspaceMember, workspace *model.Workspace, subscription *model.Subscription, source Source, open func(workspaceID, workspaceSlug string) (string, error)) (string, error) {
	if err := ManageBilling(member); err != nil {
		return "", err
	}
	if subscription == nil || subscription.ProviderCustomerID == nil || *subscription.ProviderCustomerID == "" {
		return "", ErrNoBillingAccount
	}
	managementURL, err := open(workspace.ID, workspace.Slug)
	if err != nil {
		slog.Error("failed to get billing management URL", "error", err, "workspace_id", workspace.ID)
		return "", ErrBillingManagementFailed
	}
	s.recordBilling(workspace.ID, member.UserID, "billing.management_opened", source)
	return managementURL, nil
}

// recordBilling puts a billing action in the activity; a failure is only
// logged, the checkout or the management page is open already.
func (s *WorkspaceService) recordBilling(workspaceID, userID, action string, source Source) {
	if err := s.RecordBillingAction(workspaceID, userID, action, source); err != nil {
		slog.Warn("failed to record billing activity event", "error", err, "workspace_id", workspaceID)
	}
}
