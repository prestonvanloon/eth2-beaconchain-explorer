package utils

import (
	"eth2-exporter/types"
	"fmt"
	"gopkg.in/yaml.v2"
	"os"
	"os/signal"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// Specific to the Prysm testnet
const SecondsPerSlot = 12
const SlotsPerEpoch = 8
const GenesisTimestamp = 1573489682

// Global accessible configs
var Config *types.Config

func SlotToTime(slot uint64) time.Time {
	return time.Unix(GenesisTimestamp+int64(slot)*SecondsPerSlot, 0)
}

func EpochToTime(epoch uint64) time.Time {
	return time.Unix(GenesisTimestamp+int64(epoch)*SecondsPerSlot*SlotsPerEpoch, 0)
}

func FormatBalance(balance uint64) string {
	return fmt.Sprintf("%.2f ETH", float64(balance)/float64(1000000000))
}

func WaitForCtrlC() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}

func ReadConfig(cfg *types.Config, path string) error {
	err := readConfigFile(cfg, path)

	if err != nil {
		return err
	}

	return readConfigEnv(cfg)
}

func readConfigFile(cfg *types.Config, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("Error opening config file %v: %v", path, err)
	}

	decoder := yaml.NewDecoder(f)
	err = decoder.Decode(cfg)
	if err != nil {
		return fmt.Errorf("Error decoding config file %v: %v", path, err)
	}

	return nil
}

func readConfigEnv(cfg *types.Config) error {
	return envconfig.Process("", cfg)
}