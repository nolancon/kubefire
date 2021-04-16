package script

import (
	"context"
	"fmt"
	intconfig "github.com/innobead/kubefire/internal/config"
	"github.com/innobead/kubefire/pkg/config"
	"github.com/innobead/kubefire/pkg/util"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
)

type Type string

const (
	InstallPrerequisites        Type = "install-prerequisites.sh"
	UninstallPrerequisites      Type = "uninstall-prerequisites.sh"
	InstallPrerequisitesKubeadm Type = "install-prerequisites-kubeadm.sh"
	InstallPrerequisitesK0s     Type = "install-prerequisites-k0s.sh"
	InstallPrerequisitesK3s     Type = "install-prerequisites-k3s.sh"
	InstallPrerequisitesRKE     Type = "install-prerequisites-rke.sh"
	InstallPrerequisitesRKE2    Type = "install-prerequisites-rke2.sh"
)

var (
	downloadScriptEndpointFormat = fmt.Sprintf(
		"https://raw.githubusercontent.com/nolancon/kubefire/%s/scripts/%%s",
		intconfig.GetTagVersionForDownloadScript(intconfig.TagVersion),
	)
)

func LocalScriptFile(version string, t Type) string {
	return path.Join(config.BinDir, version, string(t))
}

func RemoteScriptUrl(script Type) string {
	return fmt.Sprintf(downloadScriptEndpointFormat, script)
}

func Download(script Type, version string, force bool) error {
	log := logrus.WithFields(
		logrus.Fields{
			"version": version,
			"force":   force,
		},
	)

	url := RemoteScriptUrl(script)
	destFile := LocalScriptFile(version, script)

	log.Infof("downloading %s to save %s", url, destFile)

	err := downloadScript(
		url,
		destFile,
		force,
	)
	if err != nil {
		return errors.WithMessagef(err, "failed to download script (%s)", script)
	}

	return nil
}

func Run(script Type, version string, beforeCallback func(cmd *exec.Cmd) error) error {
	log := logrus.WithFields(
		logrus.Fields{
			"version": version,
		},
	)
	log.Infof("running script (%s)", script)

	f := LocalScriptFile(version, script)

	log.Infof("running %s", f)
	err := runScript(f, beforeCallback)

	if err != nil {
		return errors.WithMessagef(err, "failed to run script (%s)", script)
	}

	return nil
}

func downloadScript(url string, destFile string, force bool) error {
	if _, err := os.Stat(destFile); !os.IsNotExist(err) {
		if !force {
			return nil
		}

		if err := os.RemoveAll(destFile); err != nil {
			return errors.WithStack(err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(destFile), 0755); err != nil && err != os.ErrExist {
		return errors.WithStack(err)
	}

	resp, err := http.Get(url)
	if err != nil {
		return errors.WithStack(err)
	}
	defer resp.Body.Close()

	out, err := os.Create(destFile)
	if err != nil {
		return errors.WithStack(err)
	}
	defer out.Close()

	if err := out.Chmod(0700); err != nil {
		return errors.WithStack(err)
	}

	if _, err := io.Copy(out, resp.Body); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func runScript(script string, beforeCallback func(cmd *exec.Cmd) error) error {
	if _, err := os.Stat(script); os.IsNotExist(err) {
		return errors.WithStack(err)
	}

	cmd := util.UpdateCommandDefaultLogWithInfo(
		exec.CommandContext(context.Background(), "sudo", "-E", script),
	)

	if beforeCallback != nil {
		if err := beforeCallback(cmd); err != nil {
			return errors.WithStack(err)
		}
	}

	if err := cmd.Run(); err != nil {
		return errors.WithStack(err)
	}

	return nil
}
