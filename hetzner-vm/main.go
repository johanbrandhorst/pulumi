package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cenkalti/backoff"
	"github.com/pulumi/pulumi-docker/sdk/v2/go/docker"
	hcloud "github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v2/go/pulumi"
	"go.uber.org/multierr"
	"golang.org/x/crypto/ssh"
)

const startUpScript = `#!/usr/bin/env bash

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y docker.io
systemctl start docker
systemctl enable docker
`

func main() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Println("failed to get user homedir:", err)
		return
	}
	knownHostsPath := filepath.Join(homeDir, ".ssh", "known_hosts")
	privateKeyPath := filepath.Join(homeDir, ".ssh", "id_ecdsa")
	pulumi.Run(func(ctx *pulumi.Context) error {
		key, err := hcloud.NewSshKey(
			ctx,
			"johan@johan-x1",
			&hcloud.SshKeyArgs{
				PublicKey: pulumi.String("ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBJ0o9Kf//JdyNpqEgCAUXkpsIIQfjnoTEJ9UpzluabL3O/pF/kteLjk1bBO0muPPvkFPtNPvu2O5cVrWJPL/3EU="),
				Name:      pulumi.StringPtr("johan@johan-x1"),
			},
		)
		if err != nil {
			return err
		}
		server, err := hcloud.NewServer(
			ctx,
			"cheap-vm",
			&hcloud.ServerArgs{
				SshKeys:    pulumi.StringArray{key.Name},
				Image:      pulumi.String("ubuntu-20.04"),
				Name:       pulumi.StringPtr("cheap-vm"),
				ServerType: pulumi.String("cx11"),
				Location:   pulumi.StringPtr("nbg1"),
				UserData:   pulumi.StringPtr(startUpScript),
			},
		)
		if err != nil {
			return err
		}
		dockerHost := server.Ipv4Address.ApplyString(func(ipv4Address string) string {
			dockerHost := "ssh://root@" + ipv4Address
			err := replaceKnownHostsKey(ctx, knownHostsPath, privateKeyPath, net.ParseIP(ipv4Address))
			if err != nil {
				ctx.Log.Error(fmt.Sprintf("Failed to replace known_hosts key: %v", err), nil)
			}
			return dockerHost
		})
		provider, err := docker.NewProvider(
			ctx,
			"cheap-vm-docker-host",
			&docker.ProviderArgs{
				Host: dockerHost,
			},
			pulumi.Parent(server),
		)
		if err != nil {
			return err
		}
		certsImage, err := docker.NewVolume(
			ctx,
			"certs",
			&docker.VolumeArgs{},
			pulumi.Parent(provider),
			pulumi.Provider(provider),
		)
		if err != nil {
			return err
		}
		_, err = docker.NewContainer(
			ctx,
			"grpcweb-demo",
			&docker.ContainerArgs{
				Ports: docker.ContainerPortArray{
					docker.ContainerPortArgs{
						External: pulumi.IntPtr(443),
						Internal: pulumi.Int(443),
					},
					docker.ContainerPortArgs{
						External: pulumi.IntPtr(80),
						Internal: pulumi.Int(80),
					},
				},
				Image: pulumi.String("jfbrandhorst/grpcweb-example"),
				Volumes: docker.ContainerVolumeArray{
					docker.ContainerVolumeArgs{
						ContainerPath: pulumi.String("/certs"),
						VolumeName:    certsImage.Name,
					},
				},
				Restart: pulumi.String("always"),
				Command: pulumi.StringArray{
					pulumi.String("--host"),
					pulumi.String("grpcweb.jbrandhorst.com"),
				},
			},
			pulumi.Parent(provider),
			pulumi.Provider(provider),
		)
		if err != nil {
			return err
		}
		return nil
	})
}

func replaceKnownHostsKey(ctx *pulumi.Context, knownHostsPath string, privateKeyPath string, ip net.IP) (retErr error) {
	ipv4 := ip.To4()
	if ipv4 == nil {
		return errors.New("ip was not an IPv4 address")
	}
	privateKeyPEM, err := ioutil.ReadFile(privateKeyPath)
	if err != nil {
		return fmt.Errorf("failed to read private key file: %w", err)
	}
	pk, err := ssh.ParsePrivateKey(privateKeyPEM)
	if err != nil {
		return fmt.Errorf("failed to parse private key: %w", err)
	}
	// Wait for connection to server
	bk := backoff.NewExponentialBackOff()
	bk.MaxElapsedTime = time.Minute
	bk.InitialInterval = time.Second
	bk.Multiplier = 1.3
	err = backoff.RetryNotify(
		func() error {
			client, err := ssh.Dial("tcp", net.JoinHostPort(ipv4.String(), "22"), &ssh.ClientConfig{
				User: "root",
				Auth: []ssh.AuthMethod{
					ssh.PublicKeys(pk),
				},
				HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			})
			if err != nil {
				return fmt.Errorf("failed to dial new VM: %w", err)
			}
			session, err := client.NewSession()
			if err != nil {
				return fmt.Errorf("failed to create SSH session: %w", err)
			}
			defer session.Close()
			var b bytes.Buffer
			session.Stdout = &b
			session.Stderr = &b
			err = session.Run("docker info")
			if err != nil {
				return fmt.Errorf("failed to run docker: (%s) %w", strings.TrimSpace(b.String()), err)
			}
			return nil
		},
		bk,
		func(err error, next time.Duration) {
			ctx.Log.Info(fmt.Sprintf("Couldn't dial server: %v", err), nil)
		},
	)
	if err != nil {
		return fmt.Errorf("failed to wait for server to start: %w", err)
	}
	// Remove existing IPv4 address
	out, err := exec.CommandContext(context.Background(), "ssh-keygen", "-R", ipv4.String()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to remove existing known_hosts key: %v", err)
	}
	ctx.Log.Info(string(out), nil)
	out, err = exec.CommandContext(context.Background(), "ssh-keyscan", "-4", ipv4.String()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get remote host key: %v", err)
	}
	knownHostsFile, err := os.OpenFile(knownHostsPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open known_hosts key for appending: %v", err)
	}
	defer func() {
		retErr = multierr.Append(retErr, knownHostsFile.Close())
	}()
	if _, err := knownHostsFile.Write(out); err != nil {
		return fmt.Errorf("failed to write new key file to known_hosts")
	}
	return nil
}
