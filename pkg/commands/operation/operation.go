package operation

import (
	"context"
	"crypto/tls"
	"os"
	"strings"

	"github.com/go-redis/redis/v8"
	"github.com/google/wire"
	"golang.org/x/xerrors"

	"github.com/aquasecurity/fanal/cache"
	"github.com/aquasecurity/trivy-db/pkg/metadata"
	"github.com/aquasecurity/trivy/pkg/commands/option"
	"github.com/aquasecurity/trivy/pkg/db"
	"github.com/aquasecurity/trivy/pkg/log"
	"github.com/aquasecurity/trivy/pkg/policy"
	"github.com/aquasecurity/trivy/pkg/utils"
)

// SuperSet binds cache dependencies
var SuperSet = wire.NewSet(
	cache.NewFSCache,
	wire.Bind(new(cache.LocalArtifactCache), new(cache.FSCache)),
	NewCache,
)

// Cache implements the local cache
type Cache struct {
	cache.Cache
}

// NewCache is the factory method for Cache
func NewCache(c option.CacheOption) (Cache, error) {
	if strings.HasPrefix(c.CacheBackend, "redis://") {
		log.Logger.Infof("Redis cache: %s", c.CacheBackend)
		options, err := redis.ParseURL(c.CacheBackend)
		if err != nil {
			return Cache{}, err
		}

		if (option.RedisOption{}) != c.RedisOption {
			caCert, cert, err := utils.GetTLSConfig(c.RedisCACert, c.RedisCert, c.RedisKey)
			if err != nil {
				return Cache{}, err
			}

			options.TLSConfig = &tls.Config{
				RootCAs:      caCert,
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS12,
			}
		}

		redisCache := cache.NewRedisCache(options)
		return Cache{Cache: redisCache}, nil
	}

	// standalone mode
	fsCache, err := cache.NewFSCache(utils.CacheDir())
	if err != nil {
		return Cache{}, xerrors.Errorf("unable to initialize fs cache: %w", err)
	}
	return Cache{Cache: fsCache}, nil
}

// Reset resets the cache
func (c Cache) Reset() (err error) {
	if err := c.ClearDB(); err != nil {
		return xerrors.Errorf("failed to clear the database: %w", err)
	}
	if err := c.ClearArtifacts(); err != nil {
		return xerrors.Errorf("failed to clear the artifact cache: %w", err)
	}
	return nil
}

// ClearDB clears the DB cache
func (c Cache) ClearDB() (err error) {
	log.Logger.Info("Removing DB file...")
	if err = os.RemoveAll(utils.CacheDir()); err != nil {
		return xerrors.Errorf("failed to remove the directory (%s) : %w", utils.CacheDir(), err)
	}
	return nil
}

// ClearArtifacts clears the artifact cache
func (c Cache) ClearArtifacts() error {
	log.Logger.Info("Removing artifact caches...")
	if err := c.Clear(); err != nil {
		return xerrors.Errorf("failed to remove the cache: %w", err)
	}
	return nil
}

// DownloadDB downloads the DB
func DownloadDB(appVersion, cacheDir string, quiet, skipUpdate bool) error {
	client := db.NewClient(cacheDir, quiet)
	ctx := context.Background()
	needsUpdate, err := client.NeedsUpdate(appVersion, skipUpdate)
	if err != nil {
		return xerrors.Errorf("database error: %w", err)
	}

	if needsUpdate {
		log.Logger.Info("Need to update DB")
		log.Logger.Info("Downloading DB...")
		if err = client.Download(ctx, cacheDir); err != nil {
			return xerrors.Errorf("failed to download vulnerability DB: %w", err)
		}
	}

	// for debug
	if err = showDBInfo(cacheDir); err != nil {
		return xerrors.Errorf("failed to show database info: %w", err)
	}
	return nil
}

// InitBuiltinPolicies downloads the built-in policies and loads them
func InitBuiltinPolicies(ctx context.Context, cacheDir string, quiet, skipUpdate bool) ([]string, error) {
	client, err := policy.NewClient(cacheDir, quiet)
	if err != nil {
		return nil, xerrors.Errorf("policy client error: %w", err)
	}

	needsUpdate := false
	if !skipUpdate {
		needsUpdate, err = client.NeedsUpdate()
		if err != nil {
			return nil, xerrors.Errorf("unable to check if built-in policies need to be updated: %w", err)
		}
	}

	if needsUpdate {
		log.Logger.Info("Need to update the built-in policies")
		log.Logger.Info("Downloading the built-in policies...")
		if err = client.DownloadBuiltinPolicies(ctx); err != nil {
			return nil, xerrors.Errorf("failed to download built-in policies: %w", err)
		}
	}

	policyPaths, err := client.LoadBuiltinPolicies()
	if err != nil {
		if skipUpdate {
			log.Logger.Info("No built-in policies were loaded")
			return nil, nil
		}
		return nil, xerrors.Errorf("policy load error: %w", err)
	}
	return policyPaths, nil
}

func showDBInfo(cacheDir string) error {
	m := metadata.NewClient(cacheDir)
	meta, err := m.Get()
	if err != nil {
		return xerrors.Errorf("something wrong with DB: %w", err)
	}
	log.Logger.Debugf("DB Schema: %d, UpdatedAt: %s, NextUpdate: %s, DownloadedAt: %s",
		meta.Version, meta.UpdatedAt, meta.NextUpdate, meta.DownloadedAt)
	return nil
}
