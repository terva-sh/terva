//go:build terva_web

package agent

import (
	"path/filepath"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/filelock"
	"terva.sh/terva/packages/i18n"
)

func acquireWebHome() (*filelock.Lock, error) {
	lock, acquired, err := filelock.TryAcquire(filepath.Join(config.TervaHome(), "server.lock"))
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, i18n.Errorf("terva web or desktop already owns this TERVA_HOME; stop it or use a separate TERVA_HOME")
	}
	return lock, nil
}
