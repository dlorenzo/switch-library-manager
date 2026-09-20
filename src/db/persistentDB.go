package db

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"path/filepath"
	"time"

	"github.com/firebat20/switch-library-manager/settings"
	bolt "go.etcd.io/bbolt"
	"go.uber.org/zap"
)

const (
	DB_INTERNAL_TABLENAME = "internal-metadata"
)

type PersistentDB struct {
	db *bolt.DB
}

func NewPersistentDB(baseFolder string) (*PersistentDB, error) {
	// Open the my.db data file in your current directory.
	// It will be created if it doesn't exist.
	db, err := bolt.Open(filepath.Join(baseFolder, "slm.db"), 0600, &bolt.Options{Timeout: 60 * time.Second})
	if err != nil {
		// Do not log.Fatal here: this is a library constructor and killing the
		// whole process (e.g. because slm.db is locked by another instance)
		// prevents callers from handling the error gracefully.
		zap.S().Errorf("failed to open slm.db: %v", err)
		return nil, err
	}

	//set DB version
	err = db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(DB_INTERNAL_TABLENAME))
		if b == nil {
			var err error
			b, err = tx.CreateBucket([]byte(DB_INTERNAL_TABLENAME))
			if b == nil || err != nil {
				return fmt.Errorf("create bucket: %s", err)
			}
			err = b.Put([]byte("app_version"), []byte(settings.SLM_VERSION))
			if err != nil {
				zap.S().Warnf("failed to save app_version - %v", err)
				return err
			}
		}
		return nil
	})
	if err != nil {
		zap.S().Errorf("failed to initialize DB version: %v", err)
	}

	return &PersistentDB{db: db}, nil
}

func (pd *PersistentDB) Close() {
	pd.db.Close()
}

func (pd *PersistentDB) ClearTable(tableName string) error {
	err := pd.db.Update(func(tx *bolt.Tx) error {
		err := tx.DeleteBucket([]byte(tableName))
		return err
	})
	return err
}

func (pd *PersistentDB) AddEntry(tableName string, key string, value interface{}) error {
	var err error
	err = pd.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(tableName))
		if b == nil {
			b, err = tx.CreateBucket([]byte(tableName))
			if b == nil || err != nil {
				return fmt.Errorf("create bucket: %s", err)
			}
		}
		var bytesBuff bytes.Buffer
		encoder := gob.NewEncoder(&bytesBuff)
		err := encoder.Encode(value)
		if err != nil {
			return err
		}
		err = b.Put([]byte(key), bytesBuff.Bytes())
		return err
	})
	return err
}

func (pd *PersistentDB) AddEntries(tableName string, entries map[string]interface{}) error {
	if len(entries) == 0 {
		return nil
	}
	err := pd.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(tableName))
		if b == nil {
			var err error
			b, err = tx.CreateBucket([]byte(tableName))
			if b == nil || err != nil {
				return fmt.Errorf("create bucket: %s", err)
			}
		}
		for key, value := range entries {
			var bytesBuff bytes.Buffer
			encoder := gob.NewEncoder(&bytesBuff)
			err := encoder.Encode(value)
			if err != nil {
				return err
			}
			err = b.Put([]byte(key), bytesBuff.Bytes())
			if err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

func (pd *PersistentDB) GetEntry(tableName string, key string, value interface{}) error {
	err := pd.db.View(func(tx *bolt.Tx) error {

		b := tx.Bucket([]byte(tableName))
		if b == nil {
			return nil
		}
		v := b.Get([]byte(key))
		if v == nil {
			return nil
		}
		d := gob.NewDecoder(bytes.NewReader(v))

		// Decoding the serialized data
		err := d.Decode(value)
		if err != nil {
			return err
		}
		return nil
	})
	return err
}

// GetRawTable loads every key/value pair in a bucket within a single read
// transaction, returning the raw (still gob-encoded) values. This lets hot
// paths - e.g. the deep-scan metadata cache, which was previously read with
// one View transaction + decode per file - preload the whole bucket once and
// decode entries on demand.
//
// The returned byte slices are copies: bolt only guarantees value memory for
// the lifetime of the transaction, so we must not hand out the mmap'd slices
// directly.
func (pd *PersistentDB) GetRawTable(tableName string) (map[string][]byte, error) {
	result := map[string][]byte{}
	err := pd.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(tableName))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			valueCopy := make([]byte, len(v))
			copy(valueCopy, v)
			result[string(k)] = valueCopy
			return nil
		})
	})
	return result, err
}

/*func (pd *PersistentDB) GetEntries() (map[string]*switchfs.ContentMetaAttributes, error) {
	pd.db.View(func(tx *bolt.Tx) error {
		// Assume bucket exists and has keys
		b := tx.Bucket([]byte(METADATA_TABLENAME))

		c := b.Cursor()

		for k, v := c.First(); k != nil; k, v = c.Next() {
			fmt.Printf("key=%s, value=%s\n", k, v)
		}

		return nil
	})
}*/
