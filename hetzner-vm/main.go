package main

import (
	"github.com/pulumi/pulumi-docker/sdk/v2/go/docker"
	hcloud "github.com/pulumi/pulumi-hcloud/sdk/go/hcloud"
	"github.com/pulumi/pulumi/sdk/v2/go/pulumi"
)

func main() {
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
			},
		)
		if err != nil {
			return err
		}
		dockerHost := server.Ipv4Address.ApplyString(func(v string) string {
			return "ssh://root@" + v
		})
		provider, err := docker.NewProvider(
			ctx,
			"cheap-vm-docker-host",
			&docker.ProviderArgs{
				Host: dockerHost,
			},
		)
		if err != nil {
			return err
		}
		certsImage, err := docker.NewVolume(
			ctx,
			"certs",
			&docker.VolumeArgs{},
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
			pulumi.Provider(provider),
		)
		if err != nil {
			return err
		}
		return nil

	})
}
