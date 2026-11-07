package access

var Product = Catalog{
	Signup: SignupPolicy{
		Mode:          SignupTrial,
		TrialPlanID:   PlanPro,
		TrialDays:     30,
		TrialFallback: FallbackFree,
	},
	Lifecycle: LifecyclePolicy{
		PastDueGraceDays: 7,
		PaidFallback:     FallbackFree,
	},
	Plans: []Plan{
		{
			ID:          PlanFree,
			Name:        "Free",
			Description: "Read-only after the trial; your apps keep running",
			Limits:      map[Limit]int{LimitServers: 0, LimitMembers: Unlimited},
			Benefits: []string{
				"Your apps keep running, and so do their backups",
				"See apps, deploys, logs and data",
				"Stop and start apps",
			},
		},
		{
			ID:           PlanPro,
			Name:         "moin Hosted",
			Description:  "Deploys, backups and data for your servers",
			MonthlyPrice: 2900,
			YearlyPrice:  24900,
			Features:     []Feature{FeatureChanges},
			Limits:       map[Limit]int{LimitServers: Unlimited, LimitMembers: Unlimited},
			Benefits: []string{
				"Unlimited servers and apps",
				"Your whole team, no seat price",
				"Backups of every change, checked every day",
				"Data browser, SQL and the GitHub App",
			},
			Purchasable: true,
		},
	},
}

// SelfHosted is the catalog of a control plane with billing off: one free
// plan with everything.
var SelfHosted = Catalog{
	Signup:    SignupPolicy{Mode: SignupFreemium},
	Lifecycle: LifecyclePolicy{PaidFallback: FallbackFree},
	Plans: []Plan{{
		ID:          PlanFree,
		Name:        "Self-hosted",
		Description: "Everything, on your own control plane",
		Features:    []Feature{FeatureChanges},
		Limits:      map[Limit]int{LimitServers: Unlimited, LimitMembers: Unlimited},
	}},
}

type Catalog struct {
	Signup    SignupPolicy
	Lifecycle LifecyclePolicy
	Plans     []Plan
}

func PlanByID(planID string) (Plan, bool) {
	for _, plan := range Product.Plans {
		if plan.ID == planID {
			return plan, true
		}
	}
	return Plan{}, false
}
