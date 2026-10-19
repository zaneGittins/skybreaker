package controld

import (
	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/proxy/broker"
)

// Check validates the config and its secrets as serve would, without
// starting anything, so an install or upgrade can refuse a config the new
// binary won't run.
func Check(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	return broker.Check(cfg.Policy, cfg.SecretsFile)
}
