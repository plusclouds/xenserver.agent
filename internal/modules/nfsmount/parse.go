package nfsmount

import "strings"

// parseProcMounts parses /proc/mounts-style content, returning only entries
// whose filesystem type starts with "nfs" (covers "nfs" and "nfs4").
func parseProcMounts(data string) []MountInfo {
	var mounts []MountInfo
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		source, mountPoint, fsType := fields[0], fields[1], fields[2]
		if !strings.HasPrefix(fsType, "nfs") {
			continue
		}

		server, exportPath := source, ""
		if idx := strings.Index(source, ":"); idx >= 0 {
			server, exportPath = source[:idx], source[idx+1:]
		}

		mounts = append(mounts, MountInfo{
			Server:     server,
			ExportPath: exportPath,
			MountPoint: mountPoint,
			FSType:     fsType,
		})
	}
	return mounts
}
