// Generates rsrc_windows_amd64.syso (icon, manifest, version info).
package main

import (
	"image/png"
	"log"
	"os"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
)

func main() {
	f, err := os.Open("winres/icon.png")
	if err != nil {
		log.Fatal(err)
	}
	img, err := png.Decode(f)
	if err != nil {
		log.Fatal(err)
	}
	icon, err := winres.NewIconFromResizedImage(img, nil)
	if err != nil {
		log.Fatal(err)
	}
	rs := winres.ResourceSet{}
	rs.SetIcon(winres.ID(1), icon)
	rs.SetManifest(winres.AppManifest{
		Identity:            winres.AssemblyIdentity{Name: "DriveNotes", Version: [4]uint16{1, 6, 4, 0}},
		Description:         "DriveNotes",
		Compatibility:       winres.Win10AndAbove,
		ExecutionLevel:      winres.AsInvoker,
		DPIAwareness:        winres.DPIPerMonitorV2,
		UseCommonControlsV6: true,
	})
	v := version.Info{}
	v.SetFileVersion("1.6.4.0")
	v.SetProductVersion("1.6.4.0")
	_ = v.Set(version.LangDefault, version.ProductName, "DriveNotes")
	_ = v.Set(version.LangDefault, version.FileDescription, "DriveNotes")
	_ = v.Set(version.LangDefault, version.OriginalFilename, "DriveNotes.exe")
	_ = v.Set(version.LangDefault, version.InternalName, "DriveNotes")
	_ = v.Set(version.LangDefault, version.CompanyName, "DriveNotes (open source)")
	_ = v.Set(version.LangDefault, version.LegalCopyright, "Open source - github.com/holden-langenhagen/DriveNotes")
	_ = v.Set(version.LangDefault, version.Comments, "Notes app that syncs with Google Docs")
	rs.SetVersionInfo(v)
	out, err := os.Create("rsrc_windows_amd64.syso")
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	if err := rs.WriteObject(out, winres.ArchAMD64); err != nil {
		log.Fatal(err)
	}
}
