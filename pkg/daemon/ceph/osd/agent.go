/*
Copyright 2016 The Rook Authors. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package osd

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	rookalpha "github.com/rook/rook/pkg/apis/rook.io/v1alpha1"
	"github.com/rook/rook/pkg/clusterd"
	"github.com/rook/rook/pkg/daemon/ceph/client"
	"github.com/rook/rook/pkg/daemon/ceph/mon"
	oposd "github.com/rook/rook/pkg/operator/cluster/ceph/osd"
	"github.com/rook/rook/pkg/operator/cluster/ceph/osd/config"
	"github.com/rook/rook/pkg/operator/k8sutil"
	"github.com/rook/rook/pkg/util"
	"github.com/rook/rook/pkg/util/proc"
)

const (
	osdAgentName    = "osd"
	deviceKey       = "device"
	dirKey          = "dir"
	unassignedOSDID = -1
)

type OsdAgent struct {
	cluster           *mon.ClusterInfo
	nodeName          string
	forceFormat       bool
	location          string
	osdProc           map[int]*proc.MonitoredProc
	devices           string
	usingDeviceFilter bool
	metadataDevice    string
	directories       string
	procMan           *proc.ProcManager
	storeConfig       rookalpha.StoreConfig
	kv                *k8sutil.ConfigMapKVStore
	configCounter     int32
	osdsCompleted     chan struct{}
	prepareOnly       bool
}

func NewAgent(context *clusterd.Context, devices string, usingDeviceFilter bool, metadataDevice, directories string, forceFormat bool,
	location string, storeConfig rookalpha.StoreConfig, cluster *mon.ClusterInfo, nodeName string, kv *k8sutil.ConfigMapKVStore, prepareOnly bool) *OsdAgent {

	return &OsdAgent{devices: devices, usingDeviceFilter: usingDeviceFilter, metadataDevice: metadataDevice,
		directories: directories, forceFormat: forceFormat, location: location, storeConfig: storeConfig,
		cluster: cluster, nodeName: nodeName, kv: kv,
		procMan: proc.New(context.Executor), osdProc: make(map[int]*proc.MonitoredProc),
		prepareOnly: prepareOnly,
	}
}

func (a *OsdAgent) configureDirs(context *clusterd.Context, dirs map[string]int) ([]oposd.OSDInfo, error) {
	var osds []oposd.OSDInfo
	if len(dirs) == 0 {
		return osds, nil
	}

	succeeded := 0
	var lastErr error
	for dirPath, osdID := range dirs {
		config := &osdConfig{id: osdID, configRoot: dirPath, dir: true, storeConfig: a.storeConfig,
			kv: a.kv, storeName: config.GetConfigStoreName(a.nodeName)}

		if config.id == unassignedOSDID {
			// the osd hasn't been registered with ceph yet, do so now to give it a cluster wide ID
			osdID, osdUUID, err := registerOSD(context, a.cluster.Name)
			if err != nil {
				return osds, err
			}

			dirs[dirPath] = *osdID
			config.id = *osdID
			config.uuid = *osdUUID
		}

		osd, err := a.startOSD(context, config)
		if err != nil {
			logger.Errorf("failed to config osd in path %s. %+v", dirPath, err)
			lastErr = err
		} else {
			succeeded++
			osds = append(osds, *osd)
		}
	}

	logger.Infof("%d/%d osd dirs succeeded on this node", succeeded, len(dirs))
	return osds, lastErr
}

func (a *OsdAgent) removeDirs(context *clusterd.Context, removedDirs map[string]int) ([]oposd.OSDInfo, error) {
	var osds []oposd.OSDInfo
	if len(removedDirs) == 0 {
		return osds, nil
	}

	var errorMessages []string

	// walk through each of the directories and remove the OSD associated with them
	for dir, osdID := range removedDirs {
		config := &osdConfig{id: osdID, configRoot: dir, dir: true, storeConfig: a.storeConfig,
			kv: a.kv, storeName: config.GetConfigStoreName(a.nodeName)}

		if err := a.removeOSD(context, config); err != nil {
			errMsg := fmt.Sprintf("failed to remove osd.%d. %+v", osdID, err)
			logger.Error(errMsg)
			errorMessages = append(errorMessages, errMsg)
			continue
		}
	}

	if len(errorMessages) > 0 {
		// at least one OSD failed, return an overall error
		return osds, fmt.Errorf(strings.Join(errorMessages, "\n"))
	}

	return osds, nil
}

func (a *OsdAgent) configureDevices(context *clusterd.Context, devices *DeviceOsdMapping) ([]oposd.OSDInfo, error) {
	var osds []oposd.OSDInfo
	if devices == nil || len(devices.Entries) == 0 {
		return osds, nil
	}

	// compute an OSD layout scheme that will optimize performance
	scheme, err := a.getPartitionPerfScheme(context, devices)
	logger.Debugf("partition scheme: %+v, err: %+v", scheme, err)
	if err != nil {
		return osds, fmt.Errorf("failed to get OSD partition scheme: %+v", err)
	}

	if scheme.Metadata != nil {
		// partition the dedicated metadata device
		if err := partitionMetadata(context, scheme.Metadata, a.kv, config.GetConfigStoreName(a.nodeName)); err != nil {
			return osds, fmt.Errorf("failed to partition metadata %+v: %+v", scheme.Metadata, err)
		}
	}

	// initialize and start all the desired OSDs using the computed scheme
	succeeded := 0
	for _, entry := range scheme.Entries {
		config := &osdConfig{id: entry.ID, uuid: entry.OsdUUID, configRoot: context.ConfigDir,
			partitionScheme: entry, storeConfig: a.storeConfig, kv: a.kv, storeName: config.GetConfigStoreName(a.nodeName)}
		osd, err := a.startOSD(context, config)
		if err != nil {
			return osds, fmt.Errorf("failed to config osd %d. %+v", entry.ID, err)
		} else {
			succeeded++
			osds = append(osds, *osd)
		}
	}

	logger.Infof("%d/%d osd devices succeeded on this node", succeeded, len(scheme.Entries))
	return osds, nil
}

func (a *OsdAgent) removeDevices(context *clusterd.Context, removedDevicesScheme *config.PerfScheme) ([]oposd.OSDInfo, error) {
	var osds []oposd.OSDInfo
	if removedDevicesScheme == nil || len(removedDevicesScheme.Entries) == 0 {
		return osds, nil
	}

	var errorMessages []string

	// now start removing each OSD since they should now be running
	for _, entry := range removedDevicesScheme.Entries {
		cfg := &osdConfig{id: entry.ID, uuid: entry.OsdUUID, configRoot: context.ConfigDir,
			partitionScheme: entry, storeConfig: a.storeConfig, kv: a.kv, storeName: config.GetConfigStoreName(a.nodeName)}

		if err := a.removeOSD(context, cfg); err != nil {
			errMsg := fmt.Sprintf("failed to remove osd.%d. %+v", entry.ID, err)
			logger.Error(errMsg)
			errorMessages = append(errorMessages, errMsg)
			continue
		}

		// remove OSD from partition scheme map
		if err := config.RemoveFromScheme(entry, a.kv, config.GetConfigStoreName(a.nodeName)); err != nil {
			errMsg := fmt.Sprintf("failed to remove osd.%d from scheme. %+v", entry.ID, err)
			logger.Error(errMsg)
			errorMessages = append(errorMessages, errMsg)
			continue
		}
	}

	if len(errorMessages) > 0 {
		// at least one OSD failed, return an overall error
		return osds, fmt.Errorf(strings.Join(errorMessages, "\n"))
	}

	return osds, nil
}

// computes a partitioning scheme for all the given desired devices.  This could be devics already in use,
// devices dedicated to metadata, and devices with all bluestore partitions collocated.
func (a *OsdAgent) getPartitionPerfScheme(context *clusterd.Context, devices *DeviceOsdMapping) (*config.PerfScheme, error) {

	// load the existing (committed) partition scheme from disk
	perfScheme, err := config.LoadScheme(a.kv, config.GetConfigStoreName(a.nodeName))
	if err != nil {
		return nil, fmt.Errorf("failed to load partition scheme: %+v", err)
	}

	nameToUUID := map[string]string{}
	for _, disk := range context.Devices {
		if disk.UUID != "" {
			nameToUUID[disk.Name] = disk.UUID
		}
	}

	numDataNeeded := 0
	var metadataEntry *DeviceOsdIDEntry

	// enumerate the device to OSD mapping to see if we have any new data devices to create and any
	// metadata devices to store their metadata on
	for name, mapping := range devices.Entries {
		if isDeviceInUse(name, nameToUUID, perfScheme) {
			// device is already in use for either data or metadata, update the details for each of its partitions
			// (i.e. device name could have changed)
			refreshDeviceInfo(name, nameToUUID, perfScheme)
		} else if isDeviceDesiredForData(mapping) {
			// device needs data partitioning
			numDataNeeded++
		} else if isDeviceDesiredForMetadata(mapping, perfScheme) {
			// device is desired to store metadata for other OSDs
			if perfScheme.Metadata != nil {
				// TODO: this perf scheme creation algorithm assumes either zero or one metadata device, enhance to allow multiple
				// https://github.com/rook/rook/issues/341
				return nil, fmt.Errorf("%s is desired for metadata, but %s (%s) is already the metadata device",
					name, perfScheme.Metadata.Device, perfScheme.Metadata.DiskUUID)
			}

			metadataEntry = mapping
			perfScheme.Metadata = config.NewMetadataDeviceInfo(name)
		}
	}

	if numDataNeeded > 0 {
		// register each data device and compute its desired partition scheme
		for name, mapping := range devices.Entries {
			if !isDeviceDesiredForData(mapping) || isDeviceInUse(name, nameToUUID, perfScheme) {
				continue
			}

			// register/create the OSD with ceph, which will assign it a cluster wide ID
			osdID, osdUUID, err := registerOSD(context, a.cluster.Name)
			if err != nil {
				return nil, fmt.Errorf("failed to register OSD for device %s: %+v", name, err)
			}

			schemeEntry := config.NewPerfSchemeEntry(a.storeConfig.StoreType)
			schemeEntry.ID = *osdID
			schemeEntry.OsdUUID = *osdUUID

			if metadataEntry != nil && perfScheme.Metadata != nil {
				// we have a metadata device, so put the metadata partitions on it and the data partition on its own disk
				metadataEntry.Metadata = append(metadataEntry.Metadata, *osdID)
				mapping.Data = *osdID

				// populate the perf partition scheme entry with distributed partition details
				err := config.PopulateDistributedPerfSchemeEntry(schemeEntry, name, perfScheme.Metadata, a.storeConfig)
				if err != nil {
					return nil, fmt.Errorf("failed to create distributed perf scheme entry for %s: %+v", name, err)
				}
			} else {
				// there is no metadata device to use, store everything on the data device

				// update the device OSD mapping, saying this device will store the current OSDs data and metadata
				mapping.Data = *osdID
				mapping.Metadata = []int{*osdID}

				// populate the perf partition scheme entry with collocated partition details
				err := config.PopulateCollocatedPerfSchemeEntry(schemeEntry, name, a.storeConfig)
				if err != nil {
					return nil, fmt.Errorf("failed to create collocated perf scheme entry for %s: %+v", name, err)
				}
			}

			perfScheme.Entries = append(perfScheme.Entries, schemeEntry)
		}
	}

	return perfScheme, nil
}

// determines if the given device name is already in use with existing/committed partitions
func isDeviceInUse(name string, nameToUUID map[string]string, scheme *config.PerfScheme) bool {
	parts := findPartitionsForDevice(name, nameToUUID, scheme)
	return len(parts) > 0
}

// determines if the given device OSD mapping is in need of a data partition (and possibly collocated metadata partitions)
func isDeviceDesiredForData(mapping *DeviceOsdIDEntry) bool {
	if mapping == nil {
		return false
	}

	return (mapping.Data == unassignedOSDID && mapping.Metadata == nil) ||
		(mapping.Data > unassignedOSDID && len(mapping.Metadata) == 1)
}

func isDeviceDesiredForMetadata(mapping *DeviceOsdIDEntry, scheme *config.PerfScheme) bool {
	return mapping.Data == unassignedOSDID && mapping.Metadata != nil && len(mapping.Metadata) == 0
}

// finds all the partition details that are on the given device name
func findPartitionsForDevice(name string, nameToUUID map[string]string, scheme *config.PerfScheme) []*config.PerfSchemePartitionDetails {
	if scheme == nil {
		return nil
	}

	diskUUID, ok := nameToUUID[name]
	if !ok {
		return nil
	}

	parts := []*config.PerfSchemePartitionDetails{}
	for _, e := range scheme.Entries {
		for _, p := range e.Partitions {
			if p.DiskUUID == diskUUID {
				parts = append(parts, p)
			}
		}
	}

	return parts
}

// if a device name has changed, this function will find all partition entries with the device's static UUID and
// then update the device name on them
func refreshDeviceInfo(name string, nameToUUID map[string]string, scheme *config.PerfScheme) {
	parts := findPartitionsForDevice(name, nameToUUID, scheme)
	if len(parts) == 0 {
		return
	}

	// make sure each partition that is using the given device has its most up to date name
	for _, p := range parts {
		p.Device = name
	}

	// also update the device name if the given device is in use as the metadata device
	if scheme.Metadata != nil {
		if diskUUID, ok := nameToUUID[name]; ok {
			if scheme.Metadata.DiskUUID == diskUUID {
				scheme.Metadata.Device = name
			}
		}
	}
}

func (a *OsdAgent) startOSD(context *clusterd.Context, cfg *osdConfig) (*oposd.OSDInfo, error) {

	cfg.rootPath = getOSDRootDir(cfg.configRoot, cfg.id)

	// if the osd is using filestore on a device and it's previously been formatted/partitioned,
	// go ahead and remount the device now.
	if err := remountFilestoreDeviceIfNeeded(context, cfg); err != nil {
		return nil, err
	}

	// prepare the osd root dir, which will tell us if it's a new osd
	newOSD, err := prepareOSDRoot(cfg)
	if err != nil {
		return nil, err
	}

	if newOSD {
		if cfg.partitionScheme != nil {
			// format and partition the device if needed
			savedScheme, err := config.LoadScheme(a.kv, config.GetConfigStoreName(a.nodeName))
			if err != nil {
				return nil, fmt.Errorf("failed to load the saved partition scheme from %s: %+v", cfg.configRoot, err)
			}

			skipFormat := false
			for _, savedEntry := range savedScheme.Entries {
				if savedEntry.ID == cfg.id {
					// this OSD has already had its partitions created, skip formatting
					skipFormat = true
					break
				}
			}

			if !skipFormat {
				err = formatDevice(context, cfg, a.forceFormat, a.storeConfig)
				if err != nil {
					return nil, fmt.Errorf("failed format/partition of osd %d. %+v", cfg.id, err)
				}

				logger.Notice("waiting after partition/format...")
				<-time.After(2 * time.Second)
			}
		}

		// osd_data_dir/ready does not exist yet, create/initialize the OSD
		err := initializeOSD(cfg, context, a.cluster, a.location, a.prepareOnly)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize OSD at %s: %+v", cfg.rootPath, err)
		}
	} else {
		// update the osd config file
		err := writeConfigFile(cfg, context, a.cluster, a.location, a.prepareOnly)
		if err != nil {
			logger.Warningf("failed to update config file. %+v", err)
		}

		// osd_data_dir/ready already exists, meaning the OSD is already set up.
		// look up some basic information about it so we can run it.
		err = loadOSDInfo(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to get OSD information from %s: %+v", cfg.rootPath, err)
		}
	}
	osdInfo := getOSDInfo(a.cluster.Name, cfg)
	if a.prepareOnly {
		logger.Infof("done with preparing osd %v", osdInfo)
		return osdInfo, nil
	}
	// run the OSD in a child process now that it is fully initialized and ready to go
	err = a.runOSD(osdInfo)
	if err != nil {
		return osdInfo, fmt.Errorf("failed to run osd %d: %+v", cfg.id, err)
	}

	return osdInfo, nil
}

func prepareOSDRoot(cfg *osdConfig) (newOSD bool, err error) {
	newOSD = isOSDDataNotExist(cfg.rootPath)
	if !newOSD {
		// osd is not new (it's ready), nothing to prepare
		return newOSD, nil
	}

	// osd is new (it's not ready), make sure there is no stale state in the OSD dir by deleting the entire thing
	logger.Infof("osd.%d appears to be new, cleaning the root dir at %s", cfg.id, cfg.rootPath)
	if err := os.RemoveAll(cfg.rootPath); err != nil {
		logger.Warningf("failed to clean osd.%d root dir at %s, will proceed with starting osd: %+v", cfg.id, cfg.rootPath, err)
	}

	// prepare the osd dir by creating it now
	if err := os.MkdirAll(cfg.rootPath, 0744); err != nil {
		return newOSD, fmt.Errorf("failed to make osd.%d config at %s: %+v", cfg.id, cfg.rootPath, err)
	}

	return newOSD, nil
}

func getOSDInfo(clusterName string, config *osdConfig) *oposd.OSDInfo {
	confFile := getOSDConfFilePath(config.rootPath, clusterName)
	util.WriteFileToLog(logger, confFile)
	osd := &oposd.OSDInfo{
		ID:          config.id,
		DataPath:    config.rootPath,
		Config:      confFile,
		Cluster:     clusterName,
		KeyringPath: getOSDKeyringPath(config.rootPath),
		UUID:        config.uuid.String(),
		IsFileStore: isFilestore(config),
	}

	if isFilestore(config) {
		osd.Journal = getOSDJournalPath(config.rootPath)
	}
	return osd
}

// runs an OSD with the given config in a child process
func (a *OsdAgent) runOSD(osdInfo *oposd.OSDInfo) error {
	// start the OSD daemon in the foreground with the given config
	logger.Infof("starting osd %s at %s", osdInfo.ID, osdInfo.DataPath)

	osdUUIDArg := fmt.Sprintf("--osd-uuid=%s", osdInfo.UUID)
	params := []string{"--foreground",
		fmt.Sprintf("--id=%d", osdInfo.ID),
		fmt.Sprintf("--cluster=%s", osdInfo.Cluster),
		fmt.Sprintf("--osd-data=%s", osdInfo.DataPath),
		fmt.Sprintf("--conf=%s", osdInfo.Config),
		fmt.Sprintf("--keyring=%s", osdInfo.KeyringPath),
		osdUUIDArg,
	}

	if osdInfo.IsFileStore {
		params = append(params, fmt.Sprintf("--osd-journal=%s", osdInfo.Journal))
	}

	process, err := a.procMan.Start(
		fmt.Sprintf("osd%d", osdInfo.ID),
		"ceph-osd",
		regexp.QuoteMeta(osdUUIDArg),
		proc.ReuseExisting,
		params...)
	if err != nil {
		return fmt.Errorf("failed to start osd %d: %+v", osdInfo.ID, err)
	}

	if process != nil {
		// if the process was already running Start will return nil in which case we don't want to overwrite it
		a.osdProc[osdInfo.ID] = process
	}

	return nil
}

func (a *OsdAgent) removeOSD(context *clusterd.Context, config *osdConfig) error {
	// get a baseline for OSD usage so we can compare usage to it later on to know when migration has started
	initialUsage, err := client.GetOSDUsage(context, a.cluster.Name)
	if err != nil {
		logger.Warningf("failed to get baseline OSD usage, but will still continue")
	}

	// first reweight the OSD to be 0.0, which will begin the data migration
	o, err := client.CrushReweight(context, a.cluster.Name, config.id, 0.0)
	if err != nil {
		return fmt.Errorf("failed to reweight osd.%d to 0.0: %+v. %s", config.id, err, o)
	}

	// mark the OSD as out
	if err := markOSDOut(context, a.cluster.Name, config.id); err != nil {
		return fmt.Errorf("failed to mark osd.%d out: %+v", config.id, err)
	}

	// wait for the OSDs data to be migrated
	if err := waitForRebalance(context, a.cluster.Name, config.id, initialUsage); err != nil {
		return fmt.Errorf("failed to wait for cluster rebalancing after removing osd.%d: %+v", config.id, err)
	}

	// stop the OSD process and remove it from monitoring
	if proc, ok := a.osdProc[config.id]; ok {
		if err := proc.Stop(false); err != nil {
			return fmt.Errorf("failed to stop proc for osd.%d: %+v", config.id, err)
		}
	}

	// purge the OSD from the cluster
	if err := purgeOSD(context, a.cluster.Name, config.id); err != nil {
		return fmt.Errorf("failed to purge osd.%d from the cluster: %+v", config.id, err)
	}

	// delete any backups of the OSD filesystem
	if err := deleteOSDFileSystem(config); err != nil {
		logger.Warningf("failed to delete osd.%d filesystem, it may need to be cleaned up manually: %+v", config.id, err)
	}

	// delete the OSD's local storage
	osdRootDir := getOSDRootDir(config.configRoot, config.id)
	if err := os.RemoveAll(osdRootDir); err != nil {
		logger.Warningf("failed to delete osd.%d root dir from %s, it may need to be cleaned up manually: %+v",
			config.id, osdRootDir, err)
	}

	return nil
}

func waitForRebalance(context *clusterd.Context, clusterName string, osdID int, initialUsage *client.OSDUsage) error {
	if initialUsage != nil {
		// start a retry loop to wait for rebalancing to start
		err := util.Retry(20, 5*time.Second, func() error {
			currUsage, err := client.GetOSDUsage(context, clusterName)
			if err != nil {
				return err
			}

			init := initialUsage.ByID(osdID)
			curr := currUsage.ByID(osdID)

			if init == nil || curr == nil {
				return fmt.Errorf("initial OSD usage or current OSD usage for osd.%d not found. init: %+v, curr: %+v",
					osdID, initialUsage, currUsage)
			}

			if curr.UsedKB >= init.UsedKB && curr.Pgs >= init.Pgs {
				return fmt.Errorf("current used space and pg count for osd.%d has not decreased still", osdID)
			}

			// either the used space or the number of PGs has decreased for the OSD, data rebalancing has started
			return nil
		})
		if err != nil {
			return err
		}
	}

	// wait until the cluster gets fully rebalanced again
	err := util.Retry(3000, 15*time.Second, func() error {
		// get a dump of all placement groups
		pgDump, err := client.GetPGDumpBrief(context, clusterName)
		if err != nil {
			return err
		}

		// ensure that the given OSD is no longer assigned to any placement groups
		for _, pg := range pgDump {
			if pg.UpPrimaryID == osdID {
				return fmt.Errorf("osd.%d is still up primary for pg %s", osdID, pg.ID)
			}
			if pg.ActingPrimaryID == osdID {
				return fmt.Errorf("osd.%d is still acting primary for pg %s", osdID, pg.ID)
			}
			for _, id := range pg.UpOsdIDs {
				if id == osdID {
					return fmt.Errorf("osd.%d is still up for pg %s", osdID, pg.ID)
				}
			}
			for _, id := range pg.ActingOsdIDs {
				if id == osdID {
					return fmt.Errorf("osd.%d is still acting for pg %s", osdID, pg.ID)
				}
			}
		}

		// finally, ensure the cluster gets back to a clean state, meaning rebalancing is complete
		return client.IsClusterClean(context, clusterName)
	})
	if err != nil {
		return err
	}

	return nil
}

func isOSDDataNotExist(osdDataPath string) bool {
	_, err := os.Stat(filepath.Join(osdDataPath, "ready"))
	return os.IsNotExist(err)
}

func loadOSDInfo(config *osdConfig) error {
	idFile := filepath.Join(config.rootPath, "whoami")
	idContent, err := ioutil.ReadFile(idFile)
	if err != nil {
		return fmt.Errorf("failed to read OSD ID from %s: %+v", idFile, err)
	}

	osdID, err := strconv.Atoi(strings.TrimSpace(string(idContent[:])))
	if err != nil {
		return fmt.Errorf("failed to parse OSD ID from %s with content %s: %+v", idFile, idContent, err)
	}

	uuidFile := filepath.Join(config.rootPath, "fsid")
	fsidContent, err := ioutil.ReadFile(uuidFile)
	if err != nil {
		return fmt.Errorf("failed to read UUID from %s: %+v", uuidFile, err)
	}

	osdUUID, err := uuid.Parse(strings.TrimSpace(string(fsidContent[:])))
	if err != nil {
		return fmt.Errorf("failed to parse UUID from %s with content %s: %+v", uuidFile, string(fsidContent[:]), err)
	}

	config.id = osdID
	config.uuid = osdUUID
	return nil
}

func isBluestore(config *osdConfig) bool {
	return isBluestoreDevice(config) || isBluestoreDir(config)
}

func isBluestoreDevice(cfg *osdConfig) bool {
	// A device will use bluestore unless explicitly requested to be filestore (the default is blank)
	return !cfg.dir && cfg.partitionScheme != nil && cfg.partitionScheme.StoreType != config.Filestore
}

func isBluestoreDir(cfg *osdConfig) bool {
	// A dir will use filestore unless explicitly requested to be bluestore
	return cfg.dir && cfg.storeConfig.StoreType == config.Bluestore
}

func isFilestore(cfg *osdConfig) bool {
	return isFilestoreDevice(cfg) || isFilestoreDir(cfg)
}

func isFilestoreDevice(cfg *osdConfig) bool {
	// A device will use bluestore unless explicitly requested to be filestore (the default is blank)
	return !cfg.dir && cfg.partitionScheme != nil && cfg.partitionScheme.StoreType == config.Filestore
}

func isFilestoreDir(cfg *osdConfig) bool {
	// A dir will use filestore unless explicitly requested to be bluestore (the default is blank)
	return cfg.dir && cfg.storeConfig.StoreType != config.Bluestore
}
