package settings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/magiconair/properties"
	"go.uber.org/zap"
)

var (
	keysInstance *switchKeys
	keysMu       sync.RWMutex
)

type switchKeys struct {
	keys map[string]string
}

func (k *switchKeys) GetKey(keyName string) string {
	if k == nil || k.keys == nil {
		return ""
	}
	return k.keys[strings.ToLower(keyName)]
}

func SwitchKeys() (*switchKeys, error) {
	keysMu.RLock()
	defer keysMu.RUnlock()
	if keysInstance == nil {
		return nil, errors.New("prod.keys not loaded")
	}
	return keysInstance, nil
}

func InitSwitchKeys(baseFolder string) (*switchKeys, error) {
	var (
		path string
		p *properties.Properties
		err error
	)
	logger := zap.S()

	// first, try to read the prod keys from the settings value
	settings := ReadSettings(baseFolder)
	if settings.Prodkeys != "" {
		path = settings.Prodkeys
		if !strings.HasSuffix(strings.ToLower(path), ".keys") {
			path = filepath.Join(path, "prod.keys")
		}

		logger.Infof("Trying to load prod.keys based on settings.json: %v", path)
		p, err = properties.LoadFile(path, properties.UTF8)
	}else{
		err = errors.New("prod.keys not defined in settings.json")
	}

	// second, if not found by settings look into the current folder
	if err != nil {
		path = filepath.Join(baseFolder, "prod.keys")

		logger.Infof("Trying to load prod.keys based on current folder: %v", path)
		p, err = properties.LoadFile(path, properties.UTF8)
	}

	// third, if not found in current, look in home directory
	if err != nil {
		if home, herr := os.UserHomeDir(); herr == nil {
			path = filepath.Join(home, ".switch", "prod.keys")
			logger.Infof("Trying to load prod.keys based on home directory: %v", path)
			p, err = properties.LoadFile(path, properties.UTF8)
		} else {
			logger.Infof("Unable to resolve home directory: %v", herr)
		}
	}

	if err != nil {
		logger.Info("Unable to find prod.keys")
		return nil, errors.New("Error trying to read prod.keys [reason:" + err.Error() + "]")
	}

	loaded := &switchKeys{keys: map[string]string{}}
	for _, key := range p.Keys() {
		value, _ := p.Get(key)
		loaded.keys[strings.ToLower(key)] = value
	}

	keysMu.Lock()
	keysInstance = loaded
	keysMu.Unlock()

	zap.S().Info("Loaded prod.keys")
	return loaded, nil
}
