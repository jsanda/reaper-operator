package config

import (
	"fmt"
	"strconv"
	"strings"

	api "github.com/thelastpickle/reaper-operator/api/v1alpha1"
)

func ReplicationToString(r api.ReplicationConfig) string {
	if r.SimpleStrategy != nil {
		replicationFactor := strconv.FormatInt(int64(*r.SimpleStrategy), 10)
		return fmt.Sprintf(`{'class': 'SimpleStrategy', 'replication_factor': %s}`, replicationFactor)
	} else {
		var sb strings.Builder
		dcs := make([]string, 0)
		for k, v := range *r.NetworkTopologyStrategy {
			sb.WriteString("'")
			sb.WriteString(k)
			sb.WriteString("': ")
			sb.WriteString(strconv.FormatInt(int64(v), 10))
			dcs = append(dcs, sb.String())
			sb.Reset()
		}
		return fmt.Sprintf("{'class': 'NetworkTopologyStrategy', %s}", strings.Join(dcs, ", "))
	}
}
