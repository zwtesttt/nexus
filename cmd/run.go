package cmd

import (
	"github.com/am6737/nexus/config"
	"github.com/am6737/nexus/controllers"
	"github.com/am6737/nexus/tun"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

func run(c *cli.Context) error {
	configFile := c.String("config")
	cfg, err := config.Load(configFile)
	if err != nil {
		panic(err)
	}

	logger := logrus.New()
	logger.Out = os.Stdout
	logger.SetLevel(logrus.DebugLevel)
	// 启用调用者报告
	logger.SetReportCaller(true)
	logger.SetFormatter(&logrus.TextFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		FullTimestamp:   true,
		CallerPrettyfier: func(frame *runtime.Frame) (function string, file string) {
			// 获取文件名和行号
			filename := filepath.Base(frame.File)
			return "", filename + ":" + strconv.Itoa(frame.Line)
		},
	})

	// 创建新的 DarwinTun 实例
	tunDevice, err := tun.NewDeviceFromConfig(cfg, nil)
	if err != nil {
		logger.WithError(err).Error("Error creating DarwinTun")
		return err
	}
	defer tunDevice.Close()

	ctx := c.Context
	ctrl := controllers.NewControllersManager(ctx, cfg, logger, tunDevice)
	if err := ctrl.Start(ctx); err != nil {
		panic(err)
	}
	ctrl.Shutdown()

	return nil
}
