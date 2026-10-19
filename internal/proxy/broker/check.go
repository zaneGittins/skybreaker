package broker

import (
	"net/http"
	"time"

	"github.com/zaneGittins/skybreaker/internal/proxy/config"
	"github.com/zaneGittins/skybreaker/internal/proxy/secrets"
)

// Check loads the secrets file as Open would, key files included, and fails
// if an injected secret is missing or isn't bound to its host.
func Check(policy *config.Config, secretsFile string) error {
	_, err := loadSecrets(policy, secretsFile)
	return err
}

func loadSecrets(policy *config.Config, file string) (*secrets.Store, error) {
	var specs []secrets.Spec
	if file != "" {
		var err error
		if specs, err = config.LoadSecrets(file); err != nil {
			return nil, err
		}
	}
	store, err := secrets.NewStore(specs, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		return nil, err
	}
	if err := policy.CheckSecrets(func(name string) ([]string, bool) {
		if !store.Has(name) {
			return nil, false
		}
		return store.HostsFor(name), true
	}); err != nil {
		return nil, err
	}
	return store, nil
}
