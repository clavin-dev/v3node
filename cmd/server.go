package cmd

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/wyx2685/v2node/conf"
	"github.com/wyx2685/v2node/core"
	"github.com/wyx2685/v2node/limiter"
	"github.com/wyx2685/v2node/node"
)

var (
	config string
	watch  bool
)

var serverCommand = cobra.Command{
	Use:   "server",
	Short: "Run v2node server",
	Run:   serverHandle,
	Args:  cobra.NoArgs,
}

func init() {
	serverCommand.PersistentFlags().
		StringVarP(&config, "config", "c",
			"/etc/v2node/config.json", "config file path")
	serverCommand.PersistentFlags().
		BoolVarP(&watch, "watch", "w",
			true, "watch file path change")
	command.AddCommand(&serverCommand)
}

func applyLogConfig(cfg conf.LogConfig) {
	switch cfg.Level {
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "info":
		log.SetLevel(log.InfoLevel)
	case "warn", "warning":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	}
	if cfg.Output == "" {
		return
	}
	f, err := os.OpenFile(cfg.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.WithField("err", err).Error("Open log file failed, using current output instead")
		return
	}
	if oldWriter, ok := log.StandardLogger().Out.(*os.File); ok && oldWriter != os.Stdout && oldWriter != os.Stderr {
		_ = oldWriter.Close()
	}
	log.SetOutput(f)
}

func serverHandle(_ *cobra.Command, _ []string) {
	showVersion()
	c := conf.New()
	err := c.LoadFromPath(config)
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: true,
		DisableQuote:     true,
		PadLevelText:     false,
	})
	if err != nil {
		log.WithField("err", err).Error("Load config file failed")
		return
	}
	applyLogConfig(c.LogConfig)
	// Enable pprof if configured
	if c.PprofPort != 0 {
		go func() {
			log.Infof("Starting pprof server on :%d", c.PprofPort)
			if err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", c.PprofPort), nil); err != nil {
				log.WithField("err", err).Error("pprof server failed")
			}
		}()
	}
	//init limiter
	limiter.Init()
	//get node info
	nodes, err := node.New(c.NodeConfigs)
	if err != nil {
		log.WithField("err", err).Error("Get node info failed")
		return
	}
	log.Info("Got nodes info from server")
	//core
	var reloadCh = make(chan struct{}, 1)
	v2core := core.New(c)
	v2core.ReloadCh = reloadCh
	err = v2core.Start(nodes.NodeInfos)
	if err != nil {
		log.WithField("err", err).Error("Start core failed")
		return
	}
	defer func() {
		if v2core != nil {
			_ = v2core.Close()
		}
	}()
	//node
	err = nodes.Start(c.NodeConfigs, v2core)
	if err != nil {
		log.WithField("err", err).Error("Run nodes failed")
		return
	}
	log.Info("Nodes started")
	if watch {
		// On file change, just signal reload; do not run reload concurrently here
		err = c.Watch(config, func() {
			select {
			case reloadCh <- struct{}{}:
			default: // drop if a reload is already queued
			}
		})
		if err != nil {
			log.WithField("err", err).Error("start watch failed")
			return
		}
	}
	// clear memory
	runtime.GC()

	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-osSignals:
			log.Info("收到退出信号，正在关闭程序...")
			os.Exit(0)
		case <-reloadCh:
			log.Info("收到重启信号，正在重新加载配置...")
			if err := reload(config, &nodes, &v2core); err != nil {
				log.WithField("err", err).Error("重启失败，保持当前实例继续运行")
				continue
			}
			log.Info("重启成功")
		}
	}
}

func reload(config string, nodes **node.Node, v2core **core.V2Core) error {
	oldNodes := *nodes
	oldCore := *v2core
	if oldNodes == nil || oldCore == nil {
		return fmt.Errorf("old runtime is nil")
	}
	oldConf := oldCore.Config

	// Preserve old reload channel so new core continues to receive signals
	var oldReloadCh chan struct{}
	oldReloadCh = oldCore.ReloadCh

	newConf := conf.New()
	if err := newConf.LoadFromPath(config); err != nil {
		return err
	}

	newNodes, err := node.New(newConf.NodeConfigs)
	if err != nil {
		return err
	}

	newCore := core.New(newConf)
	// Reattach reload channel
	newCore.ReloadCh = oldReloadCh
	if err := newCore.Start(newNodes.NodeInfos); err != nil {
		return err
	}

	// keep old core running while preparing switch, then minimize downtime window
	if err := oldNodes.Close(); err != nil {
		_ = newCore.Close()
		return err
	}

	if err := newNodes.Start(newConf.NodeConfigs, newCore); err != nil {
		// best-effort rollback
		if oldConf != nil {
			recoverNodes, recoverErr := node.New(oldConf.NodeConfigs)
			if recoverErr == nil {
				if startErr := recoverNodes.Start(oldConf.NodeConfigs, oldCore); startErr == nil {
					*nodes = recoverNodes
				} else {
					log.WithField("err", startErr).Error("rollback start old nodes failed")
				}
			} else {
				log.WithField("err", recoverErr).Error("rollback build old nodes failed")
			}
		}
		_ = newCore.Close()
		return err
	}

	applyLogConfig(newConf.LogConfig)
	*nodes = newNodes
	*v2core = newCore
	if err := oldCore.Close(); err != nil {
		log.WithField("err", err).Warn("close old core failed after reload")
	}

	runtime.GC()
	return nil
}
