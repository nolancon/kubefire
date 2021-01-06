package bootstrap

import (
	"fmt"
	"github.com/avast/retry-go"
	"github.com/hashicorp/go-multierror"
	interr "github.com/innobead/kubefire/internal/error"
	"github.com/innobead/kubefire/pkg/bootstrap/versionfinder"
	pkgconfig "github.com/innobead/kubefire/pkg/config"
	"github.com/innobead/kubefire/pkg/constants"
	"github.com/innobead/kubefire/pkg/data"
	"github.com/innobead/kubefire/pkg/node"
	utilssh "github.com/innobead/kubefire/pkg/util/ssh"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

var BuiltinTypes = []string{
	constants.KUBEADM,
	constants.K3S,
	constants.RKE,
	constants.RKE2,
}

type Bootstrapper interface {
	Deploy(cluster *data.Cluster, before func() error) error
	DownloadKubeConfig(cluster *data.Cluster, destDir string) (string, error)
	Prepare(cluster *data.Cluster, force bool) error
	Type() string
}

func New(bootstrapper string) Bootstrapper {
	switch bootstrapper {
	case constants.KUBEADM, "":
		return NewKubeadmBootstrapper()
	case constants.K3S:
		return NewK3sBootstrapper()
	case constants.RKE:
		return NewRKEBootstrapper()
	case constants.RKE2:
		return NewRKE2Bootstrapper()
	default:
		panic("no supported bootstrapper")
	}
}

func IsValid(bootstrapper string) bool {
	switch bootstrapper {
	case constants.KUBEADM, constants.K3S, constants.RKE, constants.RKE2:
		return true
	default:
		return false
	}
}

func GenerateSaveBootstrapperVersions(bootstrapperType string, configManager pkgconfig.Manager) (
	bootstrapperLatestVersion pkgconfig.BootstrapperVersioner,
	bootstrapperVersions []pkgconfig.BootstrapperVersioner,
	err error,
) {
	versionFinder := versionfinder.New(bootstrapperType)

	latestVersion, err := versionFinder.GetLatestVersion()
	if err != nil {
		return
	}

	// get from the cache
	bootstrapperVersion := pkgconfig.NewBootstrapperVersion(bootstrapperType, latestVersion.String())
	if _, err = os.Stat(bootstrapperVersion.LocalVersionFile()); !os.IsNotExist(err) {
		bootstrapperVersions, err = configManager.GetBootstrapperVersions(bootstrapperVersion)
		if err != nil {
			return
		}

		return
	}

	// get the versions from the bootstrapper
	var versions []*data.Version
	versions, err = versionFinder.GetVersionsAfterVersion(*latestVersion)
	if err != nil {
		err = errors.WithMessagef(err, "failed to get the supported versions after/include %s from bootstrapper %s", latestVersion.String(), bootstrapperType)
		return
	}

	switch versionFinder := versionFinder.(type) {
	case *versionfinder.KubeadmVersionFinder:

		var critoolVersions []*data.Version
		critoolVersions, err = versionFinder.GetCritoolVersionsAfterVersion(*latestVersion)
		if err != nil {
			err = errors.WithMessagef(err, "failed to get the CriTools versions after/include %s from bootstrapper %s", latestVersion.String(), bootstrapperType)
			return
		}

		var kubeReleaseToolLatestVersion *data.Version
		kubeReleaseToolLatestVersion, err = versionFinder.GetKubeReleaseToolLatestVersion(*latestVersion)
		if err != nil {
			err = errors.WithMessagef(err, "failed to get the kubernetes release tool version from bootstrapper %s", bootstrapperType)
			return
		}

		for i, v := range versions {
			bv := pkgconfig.NewKubeadmBootstrapperVersion(
				v.String(),
				critoolVersions[i].String(),
				kubeReleaseToolLatestVersion.String(),
			)
			bootstrapperVersions = append(bootstrapperVersions, bv)

			if bv.Version() == latestVersion.String() {
				bootstrapperLatestVersion = bv
			}
		}

	case *versionfinder.K3sVersionFinder:

		for _, v := range versions {
			bv := pkgconfig.NewK3sBootstrapperVersion(v.String())
			bootstrapperVersions = append(bootstrapperVersions, bv)

			if bv.Version() == latestVersion.String() {
				bootstrapperLatestVersion = bv
			}
		}

	case *versionfinder.RKEVersionFinder:

		for _, v := range versions {
			bv := pkgconfig.NewRKEBootstrapperVersion(v.String(), v.ExtraMeta["kubernetes_version"].([]string))
			bootstrapperVersions = append(bootstrapperVersions, bv)

			if bv.Version() == latestVersion.String() {
				bootstrapperLatestVersion = bv
			}
		}

	case *versionfinder.RKE2VersionFinder:

		for _, v := range versions {
			bv := pkgconfig.NewRKE2BootstrapperVersion(v.String())
			bootstrapperVersions = append(bootstrapperVersions, bv)

			if bv.Version() == latestVersion.String() {
				bootstrapperLatestVersion = bv
			}
		}
	}

	if err = configManager.SaveBootstrapperVersions(bootstrapperLatestVersion, bootstrapperVersions); err != nil {
		return
	}

	return
}

func downloadKubeConfig(nodeManager node.Manager, cluster *data.Cluster, remoteKubeConfigPath string, destDir string) (string, error) {
	logrus.Infof("downloading the kubeconfig of cluster (%s)", cluster.Name)

	firstMaster, err := nodeManager.GetNode(node.Name(cluster.Name, node.Master, 1))
	if err != nil {
		return "", err
	}

	sshClient, err := utilssh.NewClient(
		firstMaster.Name,
		cluster.Spec.Prikey,
		"root",
		firstMaster.Status.IPAddresses,
		nil,
	)
	if err != nil {
		return "", err
	}
	defer sshClient.Close()

	destPath := cluster.Spec.LocalKubeConfig()

	if destDir != "" {
		destPath = path.Join(destDir, "admin.conf")
	}

	logrus.Infof("saved the kubeconfig of cluster (%s) to %s", cluster.Name, destPath)

	if remoteKubeConfigPath == "" {
		remoteKubeConfigPath = "/etc/kubernetes/admin.conf"
	}

	if err := sshClient.Download(remoteKubeConfigPath, destPath); err != nil {
		return "", err
	}

	return destPath, nil
}

func getSupportedBootstrapperVersion(versionFinder versionfinder.Finder, configManager pkgconfig.Manager, bootstrapper Bootstrapper, version string) (pkgconfig.BootstrapperVersioner, error) {
	latestVersion, err := versionFinder.GetLatestVersion()
	if err != nil {
		return nil, err
	}

	bootstrapperVersion := pkgconfig.NewBootstrapperVersion(bootstrapper.Type(), latestVersion.String())
	versions, err := configManager.GetBootstrapperVersions(bootstrapperVersion)
	if err != nil {
		return nil, err
	}

	for _, v := range versions {
		if v.Version() == version {
			return v, nil
		}

		if strings.HasPrefix(version, data.ParseVersion(v.Version()).MajorMinorString()) {
			switch v := v.(type) {
			case *pkgconfig.KubeadmBootstrapperVersion:
				v.BootstrapperVersion = version
			}

			return v, nil
		}
	}

	return nil, errors.WithMessagef(
		interr.NotFoundError,
		fmt.Sprintf("bootstrapper=%s, version=%s", bootstrapper.Type(), version),
	)
}

func initNodes(cluster *data.Cluster, cmds []string) error {
	logrus.WithField("cluster", cluster.Name).Infoln("initializing cluster nodes")

	wgInitNodes := sync.WaitGroup{}
	wgInitNodes.Add(len(cluster.Nodes))

	chErr := make(chan error, len(cluster.Nodes))

	for _, n := range cluster.Nodes {
		logrus.WithField("node", n.Name).Infoln("initializing node")

		go func(n *data.Node) {
			defer wgInitNodes.Done()

			_ = retry.Do(func() error {
				sshClient, err := utilssh.NewClient(
					n.Name,
					cluster.Spec.Prikey,
					"root",
					n.Status.IPAddresses,
					nil,
				)
				if err != nil {
					return err
				}
				defer sshClient.Close()

				err = sshClient.Run(nil, nil, cmds...)
				if err != nil {
					chErr <- errors.WithMessagef(err, "failed on node (%s)", n.Name)
				}

				return nil
			},
				retry.Delay(10*time.Second),
				retry.MaxDelay(1*time.Minute),
			)
		}(n)
	}

	logrus.Info("waiting all nodes initialization finished")

	wgInitNodes.Wait()
	close(chErr)

	var err error
	for {
		e, ok := <-chErr
		if !ok {
			break
		}

		if e != nil {
			err = multierror.Append(err, e)
		}
	}

	return err
}
