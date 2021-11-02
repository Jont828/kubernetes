//go:build windows
// +build windows

/*
Copyright 2019 The Kubernetes Authors.

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

package stats

import (
	"fmt"
	"time"

	"github.com/Microsoft/hcsshim"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	statsapi "k8s.io/kubelet/pkg/apis/stats/v1alpha1"
)

type hcsShimInterface interface {
	GetContainers(q hcsshim.ComputeSystemQuery) ([]hcsshim.ContainerProperties, error)
	GetHNSEndpointByID(endpointID string) (*hcsshim.HNSEndpoint, error)
	OpenContainer(id string) (hcsshim.Container, error)
}

type windowshim struct{}

func (s windowshim) GetContainers(q hcsshim.ComputeSystemQuery) ([]hcsshim.ContainerProperties, error) {
	return hcsshim.GetContainers(q)
}

func (s windowshim) GetHNSEndpointByID(endpointID string) (*hcsshim.HNSEndpoint, error) {
	return hcsshim.GetHNSEndpointByID(endpointID)
}

func (s windowshim) OpenContainer(id string) (hcsshim.Container, error) {
	return hcsshim.OpenContainer(id)
}

// listContainerNetworkStats returns the network stats of all the running containers.
func (p *criStatsProvider) listContainerNetworkStats() (map[string]*statsapi.NetworkStats, error) {
	shim := newHcsShim(p)
	containers, err := shim.GetContainers(hcsshim.ComputeSystemQuery{
		Types: []string{"Container"},
	})
	if err != nil {
		return nil, err
	}

	stats := make(map[string]*statsapi.NetworkStats)
	for _, c := range containers {
		cstats, err := fetchContainerStats(shim, c)
		if err != nil {
			klog.V(4).InfoS("Failed to fetch statistics for container, continue to get stats for other containers", "containerID", c.ID, "err", err)
			continue
		}
		if len(cstats.Network) > 0 {
			stats[c.ID] = hcsStatsToNetworkStats(shim, cstats.Timestamp, cstats.Network)
		}
	}

	return stats, nil
}

func newHcsShim(p *criStatsProvider) hcsShimInterface {
	var shim hcsShimInterface
	if p.hcsshimInterface == nil {
		shim = windowshim{}
	} else {
		shim = p.hcsshimInterface.(hcsShimInterface)
	}
	return shim
}

func fetchContainerStats(hcsshimInterface hcsShimInterface, c hcsshim.ContainerProperties) (stats hcsshim.Statistics, err error) {
	var (
		container hcsshim.Container
	)
	container, err = hcsshimInterface.OpenContainer(c.ID)
	if err != nil {
		return
	}
	defer func() {
		if closeErr := container.Close(); closeErr != nil {
			if err != nil {
				err = fmt.Errorf("failed to close container after error %v; close error: %v", err, closeErr)
			} else {
				err = closeErr
			}
		}
	}()

	return container.Statistics()
}

// hcsStatsToNetworkStats converts hcsshim.Statistics.Network to statsapi.NetworkStats
func hcsStatsToNetworkStats(hcsshimInterface hcsShimInterface, timestamp time.Time, hcsStats []hcsshim.NetworkStats) *statsapi.NetworkStats {
	result := &statsapi.NetworkStats{
		Time:       metav1.NewTime(timestamp),
		Interfaces: make([]statsapi.InterfaceStats, 0),
	}

	adapters := sets.NewString()
	for _, stat := range hcsStats {
		iStat, err := hcsStatsToInterfaceStats(hcsshimInterface, stat)
		if err != nil {
			klog.InfoS("Failed to get HNS endpoint, continue to get stats for other endpoints", "endpointID", stat.EndpointId, "err", err)
			continue
		}

		// Only count each adapter once.
		if adapters.Has(iStat.Name) {
			continue
		}

		result.Interfaces = append(result.Interfaces, *iStat)
		adapters.Insert(iStat.Name)
	}

	// TODO(feiskyer): add support of multiple interfaces for getting default interface.
	if len(result.Interfaces) > 0 {
		result.InterfaceStats = result.Interfaces[0]
	}

	return result
}

// hcsStatsToInterfaceStats converts hcsshim.NetworkStats to statsapi.InterfaceStats.
func hcsStatsToInterfaceStats(hcsshimInterface hcsShimInterface, stat hcsshim.NetworkStats) (*statsapi.InterfaceStats, error) {
	endpoint, err := hcsshimInterface.GetHNSEndpointByID(stat.EndpointId)
	if err != nil {
		return nil, err
	}

	return &statsapi.InterfaceStats{
		Name:    endpoint.Name,
		RxBytes: &stat.BytesReceived,
		TxBytes: &stat.BytesSent,
	}, nil
}
