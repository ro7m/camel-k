/*
Licensed to the Apache Software Foundation (ASF) under one or more
contributor license agreements.  See the NOTICE file distributed with
this work for additional information regarding copyright ownership.
The ASF licenses this file to You under the Apache License, Version 2.0
(the "License"); you may not use this file except in compliance with
the License.  You may obtain a copy of the License at

   http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"fmt"
	"strings"

	"github.com/apache/camel-k/pkg/apis/camel/v1alpha1"
	"github.com/apache/camel-k/pkg/util/watch"

	"github.com/apache/camel-k/pkg/client"
	"github.com/apache/camel-k/pkg/install"
	"github.com/apache/camel-k/pkg/util/kubernetes"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

func newCmdInstall(rootCmdOptions *RootCmdOptions) *cobra.Command {
	impl := installCmdOptions{
		RootCmdOptions: rootCmdOptions,
	}
	cmd := cobra.Command{
		Use:   "install",
		Short: "Install Camel K on a Kubernetes cluster",
		Long:  `Installs Camel K on a Kubernetes or OpenShift cluster.`,
		RunE:  impl.install,
	}

	cmd.Flags().BoolVarP(&impl.wait, "wait", "w", false, "Waits for the platform to be running")
	cmd.Flags().BoolVar(&impl.clusterSetupOnly, "cluster-setup", false, "Execute cluster-wide operations only (may require admin rights)")
	cmd.Flags().BoolVar(&impl.skipClusterSetup, "skip-cluster-setup", false, "Skip the cluster-setup phase")
	cmd.Flags().BoolVar(&impl.exampleSetup, "example", false, "Install example integration")
	cmd.Flags().StringVar(&impl.registry, "registry", "", "A Docker registry that can be used to publish images")
	cmd.Flags().StringVarP(&impl.outputFormat, "output", "o", "", "Output format. One of: json|yaml")
	cmd.Flags().StringVar(&impl.organization, "organization", "", "A organization on the Docker registry that can be used to publish images")
	cmd.Flags().StringVar(&impl.pushSecret, "push-secret", "", "A secret used to push images to the Docker registry")
	cmd.Flags().StringSliceVar(&impl.repositories, "repository", nil, "Add a maven repository")
	cmd.Flags().StringSliceVarP(&impl.properties, "property", "p", nil, "Add a camel property")
	cmd.Flags().StringVar(&impl.camelVersion, "camel-version", "", "Set the camel version")
	cmd.Flags().StringVar(&impl.baseImage, "base-image", "", "Set the base image used to run integrations")
	cmd.Flags().StringSliceVar(&impl.contexts, "context", nil, "Add a camel context to build at startup, by default all known contexts are built")

	// completion support
	configureBashAnnotationForFlag(
		&cmd,
		"context",
		map[string][]string{
			cobra.BashCompCustom: {"__kamel_kubectl_get_known_integrationcontexts"},
		},
	)

	return &cmd
}

type installCmdOptions struct {
	*RootCmdOptions
	wait             bool
	clusterSetupOnly bool
	skipClusterSetup bool
	exampleSetup     bool
	registry         string
	outputFormat     string
	organization     string
	pushSecret       string
	camelVersion     string
	baseImage        string
	repositories     []string
	properties       []string
	contexts         []string
}

func (o *installCmdOptions) install(cmd *cobra.Command, args []string) error {
	var collection *kubernetes.Collection
	if o.outputFormat != "" {
		collection = kubernetes.NewCollection()
	}

	if !o.skipClusterSetup {
		// Let's use a client provider during cluster installation, to eliminate the problem of CRD object caching
		clientProvider := client.Provider{Get: o.NewCmdClient}

		err := install.SetupClusterwideResourcesOrCollect(o.Context, clientProvider, collection)
		if err != nil && k8serrors.IsForbidden(err) {
			fmt.Println("Current user is not authorized to create cluster-wide objects like custom resource definitions or cluster roles: ", err)

			meg := `please login as cluster-admin and execute "kamel install --cluster-setup" to install cluster-wide resources (one-time operation)`
			return errors.New(meg)
		} else if err != nil {
			return err
		}
	}

	if o.clusterSetupOnly {
		if collection == nil {
			fmt.Println("Camel K cluster setup completed successfully")
		}
	} else {
		c, err := o.GetCmdClient()
		if err != nil {
			return err
		}

		namespace := o.Namespace

		err = install.OperatorOrCollect(o.Context, c, namespace, collection)
		if err != nil {
			return err
		}

		platform, err := install.PlatformOrCollect(o.Context, c, namespace, o.registry, o.organization, o.pushSecret, collection)
		if err != nil {
			return err
		}

		if len(o.properties) > 0 {
			platform.Spec.Build.Properties = make(map[string]string)

			for _, property := range o.properties {
				kv := strings.Split(property, "=")

				if len(kv) == 2 {
					platform.Spec.Build.Properties[kv[0]] = kv[1]
				}
			}
		}
		if len(o.repositories) > 0 {
			platform.Spec.Build.Repositories = o.repositories
		}
		if o.camelVersion != "" {
			platform.Spec.Build.CamelVersion = o.camelVersion
		}
		if o.baseImage != "" {
			platform.Spec.Build.BaseImage = o.baseImage
		}

		platform.Spec.Resources.Contexts = o.contexts

		err = install.RuntimeObjectOrCollect(o.Context, c, namespace, collection, platform)
		if err != nil {
			return err
		}

		if o.exampleSetup {
			err = install.ExampleOrCollect(o.Context, c, namespace, collection)
			if err != nil {
				return err
			}
		}

		if collection == nil {
			if o.wait {
				err = o.waitForPlatformReady(platform)
				if err != nil {
					return err
				}
			}

			fmt.Println("Camel K installed in namespace", namespace)
		}
	}

	if collection != nil {
		return o.printOutput(collection)
	}

	return nil
}

func (o *installCmdOptions) printOutput(collection *kubernetes.Collection) error {
	lst := collection.AsKubernetesList()
	switch o.outputFormat {
	case "yaml":
		data, err := kubernetes.ToYAML(lst)
		if err != nil {
			return err
		}
		fmt.Print(string(data))
	case "json":
		data, err := kubernetes.ToJSON(lst)
		if err != nil {
			return err
		}
		fmt.Print(string(data))
	default:
		return errors.New("unknown output format: " + o.outputFormat)
	}
	return nil
}

func (o *installCmdOptions) waitForPlatformReady(platform *v1alpha1.IntegrationPlatform) error {
	handler := func(i *v1alpha1.IntegrationPlatform) bool {
		if i.Status.Phase != "" {
			fmt.Println("platform \""+platform.Name+"\" in phase", i.Status.Phase)

			if i.Status.Phase == v1alpha1.IntegrationPlatformPhaseReady {
				// TODO display some error info when available in the status
				return false
			}

			if i.Status.Phase == v1alpha1.IntegrationPlatformPhaseError {
				fmt.Println("platform installation failed")
				return false
			}
		}

		return true
	}

	return watch.HandlePlatformStateChanges(o.Context, platform, handler)
}
