package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newTypes(app *App) *cobra.Command {
	var region string
	var available bool
	cmd := &cobra.Command{
		Use: "types", Short: "Instance types, prices, and regions with capacity", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, err := app.Client()
			if err != nil {
				return err
			}
			types, err := c.InstanceTypes(cmd.Context())
			if err != nil {
				return err
			}
			rows := [][]string{{"TYPE", "GPU", "$/HR", "VCPU", "RAM_GiB", "DISK_GiB", "REGIONS_WITH_CAPACITY"}}
			for _, name := range sortedKeys(types) {
				t := types[name]
				if region != "" && !t.HasCapacity(region) {
					continue
				}
				if available && len(t.RegionsWithCapacityAvailable) == 0 {
					continue
				}
				it := t.InstanceType
				rows = append(rows, []string{it.Name, it.GPUDescription, usd(it), fmt.Sprint(it.Specs.VCPUs), fmt.Sprint(it.Specs.MemoryGiB), fmt.Sprint(it.Specs.StorageGiB), orDash(regionNames(t))})
			}
			table(rows)
			return nil
		},
	}
	cmd.Flags().StringVarP(&region, "region", "r", "", "only types with capacity in this region")
	cmd.Flags().BoolVarP(&available, "available", "a", false, "only types with capacity somewhere")
	return cmd
}

func newImages(app *App) *cobra.Command {
	return &cobra.Command{
		Use: "images [REGION]", Short: "Image families and ids", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := app.Client()
			if err != nil {
				return err
			}
			imgs, err := c.Images(cmd.Context())
			if err != nil {
				return err
			}
			rows := [][]string{{"FAMILY", "NAME", "VERSION", "ARCH", "REGION", "ID"}}
			for _, i := range imgs {
				if len(args) == 1 && i.Region.Name != args[0] {
					continue
				}
				rows = append(rows, []string{i.Family, i.Name, i.Version, i.Architecture, i.Region.Name, i.ID})
			}
			table(rows)
			return nil
		},
	}
}

func newKeys(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use: "keys", Short: "List ssh keys registered in Lambda", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, err := app.Client()
			if err != nil {
				return err
			}
			keys, err := c.SSHKeys(cmd.Context())
			if err != nil {
				return err
			}
			rows := [][]string{{"NAME", "ID", "PUBLIC_KEY"}}
			for _, k := range keys {
				pk := k.PublicKey
				if len(pk) > 40 {
					pk = pk[:40] + "…"
				}
				rows = append(rows, []string{k.Name, k.ID, pk})
			}
			table(rows)
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use: "add NAME FILE.pub", Short: "Register a public key with Lambda", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := app.Client()
			if err != nil {
				return err
			}
			b, err := os.ReadFile(args[1])
			if err != nil {
				return err
			}
			k, err := c.AddSSHKey(cmd.Context(), args[0], strings.TrimSpace(string(b)))
			if err != nil {
				return err
			}
			fmt.Printf("added key %s (%s)\n", k.Name, k.ID)
			return nil
		},
	})
	return cmd
}
