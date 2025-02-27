/*
 * Copyright 2023 The Kubernetes Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"

	coreclientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"sigs.k8s.io/dra-example-driver/pkg/flags"
)

const (
	DriverName = "npu.example.com"

	PluginRegistrationPath     = "/var/lib/kubelet/plugins_registry/" + DriverName + ".sock"
	DriverPluginPath           = "/var/lib/kubelet/plugins/" + DriverName
	DriverPluginSocketPath     = DriverPluginPath + "/plugin.sock"
	DriverPluginCheckpointFile = "checkpoint.json"
)

type Flags struct {
	kubeClientConfig flags.KubeClientConfig
	loggingConfig    *flags.LoggingConfig

	nodeName string
	cdiRoot  string
}

type Config struct {
	flags      *Flags
	coreclient coreclientset.Interface
}

func main() {
	if err := newApp().Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func newApp() *cli.App {
	flags := &Flags{
		loggingConfig: flags.NewLoggingConfig(),
	}

	app := &cli.App{
		Name:  "dra-example-kubeletplugin",
		Usage: "dra-example-kubeletplugin implements a DRA driver plugin for Ascend NPU.",
		Action: func(c *cli.Context) error {
			ctx := c.Context

			config := &Config{
				flags: flags,
			}

			return StartPlugin(ctx, config)
		},
	}

	return app
}

func StartPlugin(ctx context.Context, config *Config) error {
	driver, err := NewDriver(ctx, config)
	if err != nil {
		return err
	}

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	<-sigc

	err = driver.Shutdown(ctx)
	if err != nil {
		klog.FromContext(ctx).Error(err, "Unable to cleanly shutdown driver")
	}

	return nil
}
