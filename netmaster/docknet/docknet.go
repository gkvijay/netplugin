/***
Copyright 2014 Cisco Systems Inc. All rights reserved.

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

package docknet

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/contiv/netplugin/core"
	"github.com/contiv/netplugin/netmaster/mastercfg"
	"github.com/contiv/netplugin/utils"
	"github.com/samalba/dockerclient"

	log "github.com/Sirupsen/logrus"
)

const (
	defaultTenantName = "default"
	docknetOperPrefix = mastercfg.StateOperPath + "docknet/"
	docknetOperPath   = docknetOperPrefix + "%s"
)

var netDriverName = "netplugin"
var ipamDriverName = "netplugin"

// DnetOperState has oper state of docker network
type DnetOperState struct {
	core.CommonState
	TenantName  string `json:"tenantName"`
	NetworkName string `json:"networkName"`
	ServiceName string `json:"serviceName"`
	DocknetUUID string `json:"docknetUUID"`
}

// Write the state.
func (s *DnetOperState) Write() error {
	key := fmt.Sprintf(docknetOperPath, s.ID)
	return s.StateDriver.WriteState(key, s, json.Marshal)
}

// Read the state for a given identifier
func (s *DnetOperState) Read(id string) error {
	key := fmt.Sprintf(docknetOperPath, id)
	return s.StateDriver.ReadState(key, s, json.Unmarshal)
}

// ReadAll state and return the collection.
func (s *DnetOperState) ReadAll() ([]core.State, error) {
	return s.StateDriver.ReadAllState(docknetOperPrefix, s, json.Unmarshal)
}

// WatchAll state transitions and send them through the channel.
func (s *DnetOperState) WatchAll(rsps chan core.WatchState) error {
	return s.StateDriver.WatchAllState(docknetOperPrefix, s, json.Unmarshal,
		rsps)
}

// Clear removes the state.
func (s *DnetOperState) Clear() error {
	key := fmt.Sprintf(docknetOperPath, s.ID)
	return s.StateDriver.ClearState(key)
}

// CreateDockNet Creates a network in docker daemon
func CreateDockNet(tenantName, networkName, serviceName string, nwCfg *mastercfg.CfgNetworkState) error {
	var nwID string
	var subnetCIDRv6 = ""

	if nwCfg.IPv6Subnet != "" {
		subnetCIDRv6 = fmt.Sprintf("%s/%d", nwCfg.IPv6Subnet, nwCfg.IPv6SubnetLen)
	}

	// Trim default tenant name
	docknetName := GetDocknetName(tenantName, networkName, "", serviceName)

	// connect to docker
	docker, err := dockerclient.NewDockerClient("unix:///var/run/docker.sock", nil)
	if err != nil {
		log.Errorf("Unable to connect to docker. Error %v", err)
		return errors.New("Unable to connect to docker")
	}

	// Check if the network already exists
	nw, err := docker.InspectNetwork(docknetName)
	if err == nil && nw.Driver == netDriverName {
		log.Infof("docker network: %s already exists", docknetName)
		nwID = nw.ID
	} else if err == nil && nw.Driver != netDriverName {
		log.Errorf("Network name %s used by another driver %s", docknetName, nw.Driver)
		return errors.New("Network name used by another driver")
	} else if err != nil {
		// plugin options to be sent to docker
		netPluginOptions := make(map[string]string)
		netPluginOptions["tenant"] = nwCfg.Tenant
		netPluginOptions["encap"] = nwCfg.PktTagType
		if nwCfg.PktTagType == "vxlan" {
			netPluginOptions["pkt-tag"] = strconv.Itoa(nwCfg.ExtPktTag)
		} else {
			netPluginOptions["pkt-tag"] = strconv.Itoa(nwCfg.PktTag)
		}

		subnetCIDR := fmt.Sprintf("%s/%d", nwCfg.SubnetIP, nwCfg.SubnetLen)

		var ipams []dockerclient.IPAMConfig
		var IPAMv4 = dockerclient.IPAMConfig{
			Subnet:  subnetCIDR,
			Gateway: nwCfg.Gateway,
		}
		ipams = append(ipams, IPAMv4)
		var IPAMv6 dockerclient.IPAMConfig
		if subnetCIDRv6 != "" {
			IPAMv6 = dockerclient.IPAMConfig{
				Subnet:  subnetCIDRv6,
				Gateway: nwCfg.IPv6Gateway,
			}
			ipams = append(ipams, IPAMv6)
		}
		ipamOptions := make(map[string]string)
		ipamOptions["tenant"] = nwCfg.Tenant
		ipamOptions["network"] = nwCfg.NetworkName

		// Build network parameters
		nwCreate := dockerclient.NetworkCreate{
			Name:           docknetName,
			CheckDuplicate: true,
			Driver:         netDriverName,
			IPAM: dockerclient.IPAM{
				Driver:  ipamDriverName,
				Config:  ipams,
				Options: ipamOptions,
			},
			Options: netPluginOptions,
		}

		log.Infof("Creating docker network: %+v", nwCreate)

		// Create network
		resp, err := docker.CreateNetwork(&nwCreate)
		if err != nil {
			log.Errorf("Error creating network %s. Err: %v", docknetName, err)
			return err
		}

		nwID = resp.ID
	}

	// Get the state driver
	stateDriver, err := utils.GetStateDriver()
	if err != nil {
		log.Warnf("Couldn't read global config %v", err)
		return err
	}

	// save docknet oper state
	dnetOper := DnetOperState{
		TenantName:  tenantName,
		NetworkName: networkName,
		ServiceName: serviceName,
		DocknetUUID: nwID,
	}
	dnetOper.ID = fmt.Sprintf("%s.%s.%s", tenantName, networkName, serviceName)
	dnetOper.StateDriver = stateDriver

	// write the dnet oper state
	return dnetOper.Write()
}

// DeleteDockNet deletes a network in docker daemon
func DeleteDockNet(tenantName, networkName, serviceName string) error {
	// Trim default tenant name
	docknetName := GetDocknetName(tenantName, networkName, "", serviceName)

	// connect to docker
	docker, err := dockerclient.NewDockerClient("unix:///var/run/docker.sock", nil)
	if err != nil {
		log.Errorf("Unable to connect to docker. Error %v", err)
		return errors.New("Unable to connect to docker")
	}

	log.Infof("Deleting docker network: %+v", docknetName)

	// Delete network
	err = docker.RemoveNetwork(docknetName)
	if err != nil {
		log.Errorf("Error deleting network %s. Err: %v", docknetName, err)
		return err
	}

	// Get the state driver
	stateDriver, err := utils.GetStateDriver()
	if err != nil {
		log.Warnf("Couldn't read global config %v", err)
		return err
	}

	// save docknet oper state
	dnetOper := DnetOperState{}
	dnetOper.ID = fmt.Sprintf("%s.%s.%s", tenantName, networkName, serviceName)
	dnetOper.StateDriver = stateDriver

	// write the dnet oper state
	return dnetOper.Clear()
}

// FindDocknetByUUID find the docknet by UUID
func FindDocknetByUUID(dnetID string) (*DnetOperState, error) {
	// Get the state driver
	stateDriver, err := utils.GetStateDriver()
	if err != nil {
		log.Warnf("Couldn't read global config %v", err)
		return nil, err
	}

	tmpDnet := DnetOperState{}
	tmpDnet.StateDriver = stateDriver
	dnetOperList, err := tmpDnet.ReadAll()
	if err != nil {
		log.Errorf("Error getting docknet list. Err: %v", err)
		return nil, err
	}

	// Walk all dnets and find the matching UUID
	for _, dnet := range dnetOperList {
		if dnet.(*DnetOperState).DocknetUUID == dnetID {
			return dnet.(*DnetOperState), nil
		}
	}

	return nil, errors.New("docknet UUID not found")
}

// GetDocknetName gets tenant, network, epg and service name
// Encoding format: service.{epg|network}.tenant
func GetDocknetName(tenant string, network string, epg string, service string) string {

	docknetName := ""

	// if epg is specified, always use that, else use nw
	if epg == "" {
		docknetName = network
	} else {
		docknetName = epg
	}

	// add tenant suffix if not the default tenant
	if tenant != defaultTenantName {
		docknetName = docknetName + "." + tenant
	}

	// add service prefix if specified
	if service != "" {
		docknetName = service + docknetName
	}
	return docknetName
}

// ParseDocknetName gets tenant, service and network name
func ParseDocknetName(nwName string) (string, string, string, error) {
	// parse the network name
	var tenantName, netName, serviceName string
	log.Debugf("Parsing nwName: %s", nwName)
	if nwName == "" {
		log.Errorf("Invalid network name format for network %s", nwName)
		return "", "", "", errors.New("Invalid format")
	}
	if strings.Contains(nwName, "/") {
		names := strings.Split(nwName, "/")
		if len(names) == 2 {
			// has service.network/tenant format.
			tenantName = names[1]

			// parse service and network names
			sNames := strings.Split(names[0], ".")
			if len(sNames) == 2 {
				// has service.network format
				netName = sNames[1]
				serviceName = sNames[0]
			} else {
				netName = sNames[0]
			}
		} else if len(names) == 1 {
			// has service.network in default tenant
			tenantName = defaultTenantName

		} else {
			log.Errorf("Invalid network name format for network %s", nwName)
			return "", "", "", errors.New("Invalid format")
		}
		// parse service and network names from service.network
		sNames := strings.Split(names[0], ".")
		if len(sNames) == 2 {
			// has service.network format
			netName = sNames[1]
			serviceName = sNames[0]
		} else {
			netName = sNames[0]
		}
	} else {
		names := strings.Split(nwName, ".")
		// parse tenant name
		if len(names) == 2 {
			// has service__network.tenant format.
			tenantName = names[1]
		} else if len(names) == 1 {
			// has service__network in default tenant
			tenantName = defaultTenantName
		} else {
			log.Errorf("Invalid network name format for network %s", nwName)
			return "", nwName, "", errors.New("Invalid format")
		}
		// parse service and network names from service__network
		sNames := strings.Split(names[0], "__")
		if len(sNames) == 2 {
			// has service.network format
			netName = sNames[1]
			serviceName = sNames[0]
		} else {
			// network name in default tenant
			netName = sNames[0]
		}
	}
	return tenantName, netName, serviceName, nil
}
