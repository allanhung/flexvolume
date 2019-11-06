package oss

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/allanhung/flexvolume/provider/utils"
	"github.com/denverdino/aliyungo/ecs"
	log "github.com/sirupsen/logrus"
)

// OssOptions oss plugin options
type OssOptions struct {
	Bucket      string `json:"bucket"`
	SubPath     string `json:"subpath"`
	Uid         string `json:"uid"`
	Gid         string `json:"gid"`
	Url         string `json:"url"`
	OtherOpts   string `json:"otherOpts"`
	AkId        string `json:"akId"`
	AkSecret    string `json:"akSecret"`
	VolumeName  string `json:"kubernetes.io/pvOrVolumeName"`
	SecretAkId  string `json:"kubernetes.io/secret/akId"`
	SecretAkSec string `json:"kubernetes.io/secret/akSecret"`
}

// const values
const (
	CredentialFile = "/etc/passwd-ossfs"
)

// OssPlugin oss plugin
type OssPlugin struct {
	client *ecs.Client
}

// NewOptions plugin new options
func (p *OssPlugin) NewOptions() interface{} {
	return &OssOptions{}
}

// Init oss plugin init
func (p *OssPlugin) Init() utils.Result {
	return utils.Succeed()
}

// Mount Paras format:
// /usr/libexec/kubernetes/kubelet-plugins/volume/exec/ali~oss/oss
// mount
// /var/lib/kubelet/pods/e000259c-4dac-11e8-a884-04163e0f011e/volumes/alicloud~oss/oss1
// {
//   "akId":"***",
//   "akSecret":"***",
//   "bucket":"oss",
//   "kubernetes.io/fsType": "",
//   "kubernetes.io/pod.name": "nginx-oss-deploy-f995c89f4-kj25b",
//   "kubernetes.io/pod.namespace":"default",
//   "kubernetes.io/pod.uid":"e000259c-4dac-11e8-a884-04163e0f011e",
//   "kubernetes.io/pvOrVolumeName":"oss1",
//   "kubernetes.io/readwrite":"rw",
//   "kubernetes.io/serviceAccount.name":"default",
//   "otherOpts":"-o max_stat_cache_size=0 -o allow_other",
//   "url":"oss-cn-hangzhou.aliyuncs.com"
// }
func (p *OssPlugin) Mount(opts interface{}, mountPath string) utils.Result {

	// logout oss paras
	opt := opts.(*OssOptions)
	argStr := ""
	for _, tmpStr := range os.Args {
		if !strings.Contains(tmpStr, "akSecret") {
			argStr += tmpStr + ", "
		}
	}
	argStr = argStr + "VolumeName: " + opt.VolumeName + ", AkId: " + opt.AkId + ", Bucket: " + opt.Bucket + ", SubPath: " + opt.SubPath + ", Uid: " + opt.Uid + ", Gid: " + opt.Gid + ", url: " + opt.Url + ", OtherOpts: " + opt.OtherOpts
	log.Infof("Oss Plugin Mount: %s", argStr)

	if err := p.checkOptions(opt); err != nil {
		utils.FinishError("OSS: check option error: " + err.Error())
	}

	if utils.IsMounted(mountPath) {
		return utils.Result{Status: "Success"}
	}

	// Create Mount Path
	if err := utils.CreateDest(mountPath); err != nil {
		utils.FinishError("Oss, Mount fail with create Path error: " + err.Error() + mountPath)
	}

	// Save ak file for ossfs
	if err := p.saveCredential(opt); err != nil {
		utils.FinishError("Oss, Save AK file fail: " + err.Error())
	}

	// retrive podid
	podID, _, err := getPodInfoFromMountDir(mountPath)
	if err != nil {
		utils.FinishError("Oss, Get Pod Info error: " + err.Error())
	}

	// oss mount path
	ossMountPath := fmt.Sprintf("/oss/%s~%s", podID, opt.VolumeName)
	if err := utils.CreateDest(ossMountPath); err != nil {
		utils.FinishError("Oss, Create oss Mount Path error: " + err.Error() + ossMountPath)
	}
	// default use allow_other
	if !utils.IsMounted(ossMountPath) {
		mntCmd := fmt.Sprintf("systemd-run --scope -- ossfs %s %s -ourl=%s -o allow_other %s", opt.Bucket, ossMountPath, opt.Url, opt.OtherOpts)
		systemdCmd := fmt.Sprintf("which systemd-run")
		if _, err := utils.Run(systemdCmd); err != nil {
			mntCmd = fmt.Sprintf("ossfs %s %s -ourl=%s -o allow_other %s", opt.Bucket, ossMountPath, opt.Url, opt.OtherOpts)
			log.Infof("Mount oss bucket without systemd-run")
		}
		if out, err := utils.Run(mntCmd); err != nil {
			utils.FinishError("Create OSS volume fail: " + err.Error() + ", out: " + out)
		}
	}

	ossSubPath := ""
	if opt.SubPath == "" {
		ossSubPath = ossMountPath
	} else {
		ossSubPath = fmt.Sprintf("%s/%s", ossMountPath, opt.SubPath)
		// Create subpath if it does not exist
		if err := utils.CreateDest(ossSubPath); err != nil {
			utils.FinishError("Oss Subpath, Create Oss SubPath: " + ossSubPath + " error: " + err.Error())
		}
		log.Infof("Create OSS SubPath: " + ossSubPath)
		chownCmd := fmt.Sprintf("chown %s:%s %s", opt.Uid, opt.Gid, ossSubPath)
		if out, err := utils.Run(chownCmd); err != nil {
			utils.FinishError("Oss Subpath, Chown fail: " + err.Error() + ", out: " + out + ", cmd: " + chownCmd)
		}
		log.Infof("Chown with command: " + chownCmd)
	}

	// remove mount path instead of symlink
	err = os.Remove(mountPath)
	if err != nil {
		utils.FinishError("Oss Subpath, remove mount path fail: " + err.Error())
	}
	// mount subpath on bucket to mount path
	if err := os.Symlink(ossSubPath, mountPath); err != nil {
		utils.FinishError("Oss Subpath, create symlink fail: " + err.Error() + " srcPath: " + ossSubPath + " destPath: " + mountPath)
	}

	log.Info("Mount Oss successful: ", mountPath)
	return utils.Result{Status: "Success"}
}

// Unmount format
// /usr/libexec/kubernetes/kubelet-plugins/volume/exec/ali~oss/oss
// unmount
// /var/lib/kubelet/pods/e000259c-4dac-11e8-a884-00163e0f011e/volumes/alicloud~oss/oss1
func (p *OssPlugin) Unmount(mountPoint string) utils.Result {
	log.Infof("Oss Plugin Umount: %s", strings.Join(os.Args, ","))

	// check subpath volume umount if exist.
	checkSubpathVolumes(mountPoint)

	// remove symlink
	log.Infof("Unlink %s", mountPoint)
	if err := os.Remove(mountPoint); err != nil {
		utils.FinishError("Fail to unlink: " + mountPoint + " with Error: " + err.Error())
	}

	// retrive podid
	podID, volumeName, err := getPodInfoFromMountDir(mountPoint)
	if err != nil {
		utils.FinishError("Oss SubPath, Get Pod Info error: " + err.Error())
	}

	ossMountPath := fmt.Sprintf("/oss/%s~%s", podID, volumeName)

	if !utils.IsMounted(ossMountPath) {
		return utils.Succeed()
	}

	// do umount
	umntCmd := fmt.Sprintf("fusermount -u %s", ossMountPath)
	log.Infof("Unmount with command: %s", umntCmd)
	if _, err := utils.Run(umntCmd); err != nil {
		if strings.Contains(err.Error(), "Device or resource busy") {
			lazyUmntCmd := fmt.Sprintf("fusermount -uz %s", ossMountPath)
			if _, err := utils.Run(lazyUmntCmd); err != nil {
				utils.FinishError("Lazy Umount OSS Fail: " + err.Error())
			}
			log.Infof("Lazy umount Oss path successful: %s", ossMountPath)
			return utils.Succeed()
		}
		utils.FinishError("Umount OSS Fail: " + err.Error())
	}

	// remove directory
	err = os.Remove(ossMountPath)
	if err != nil {
		utils.FinishError("Oss, remove mount path fail: " + err.Error())
	}

	log.Info("Umount Oss path successful: ", mountPoint)
	return utils.Succeed()
}

// check if subPath volume exist, if subpath is mounted, umount it;
// /var/lib/kubelet/pods/6dd977d1-302a-11e9-b51c-00163e0cd246/volumes/alicloud~oss/oss1
// /var/lib/kubelet/pods/6dd977d1-302a-11e9-b51c-00163e0cd246/volume-subpaths/oss1/nginx-flexvolume-oss/0
func checkSubpathVolumes(mountPoint string) {
	podId := ""
	volumeName := filepath.Base(mountPoint)
	podsSplit := strings.Split(mountPoint, "pods")
	if len(podsSplit) >= 2 {
		volumesSplit := strings.Split(podsSplit[1], "volumes")
		if len(volumesSplit) >= 2 {
			tmpPid := volumesSplit[0]
			podId = tmpPid[1 : len(tmpPid)-1]
		}
	}
	if podId != "" {
		subPathRootDir := "/var/lib/kubelet/pods/" + podId + "/volume-subpaths/" + volumeName
		if !utils.IsFileExisting(subPathRootDir) {
			return
		}
		checkCmd := fmt.Sprintf("mount | grep %s", subPathRootDir)
		if out, err := utils.Run(checkCmd); err == nil {
			subMntList := strings.Split(out, "\n")
			for _, mntItem := range subMntList {
				strList := strings.Split(mntItem, " ")
				if len(strList) > 3 {
					mntPoint := strList[2]
					umntCmd := fmt.Sprintf("fusermount -u %s", mntPoint)
					if _, err := utils.Run(umntCmd); err != nil {
						log.Info("Umount Oss path failed: with error:", mntPoint, err.Error())
					}
				}
			}
		}
	}
}

// Attach not supported
func (p *OssPlugin) Attach(opts interface{}, nodeName string) utils.Result {
	return utils.NotSupport()
}

// Detach not support
func (p *OssPlugin) Detach(device string, nodeName string) utils.Result {
	return utils.NotSupport()
}

// Getvolumename Support
func (p *OssPlugin) Getvolumename(opts interface{}) utils.Result {
	opt := opts.(*OssOptions)
	return utils.Result{
		Status:     "Success",
		VolumeName: opt.VolumeName,
	}
}

// Waitforattach Not Support
func (p *OssPlugin) Waitforattach(devicePath string, opts interface{}) utils.Result {
	return utils.NotSupport()
}

// Mountdevice Not Support
func (p *OssPlugin) Mountdevice(mountPath string, opts interface{}) utils.Result {
	return utils.NotSupport()
}

// save ak file: bucket:ak_id:ak_secret
func (p *OssPlugin) saveCredential(options *OssOptions) error {

	oldContentByte := []byte{}
	if utils.IsFileExisting(CredentialFile) {
		tmpValue, err := ioutil.ReadFile(CredentialFile)
		if err != nil {
			return err
		}
		oldContentByte = tmpValue
	}

	oldContentStr := string(oldContentByte[:])
	newContentStr := ""
	for _, line := range strings.Split(oldContentStr, "\n") {
		lineList := strings.Split(line, ":")
		if len(lineList) != 3 || lineList[0] == options.Bucket {
			continue
		}
		newContentStr += line + "\n"
	}

	newContentStr = options.Bucket + ":" + options.AkId + ":" + options.AkSecret + "\n" + newContentStr
	if err := ioutil.WriteFile(CredentialFile, []byte(newContentStr), 0640); err != nil {
		log.Errorf("Save Credential File failed, %s, %s", newContentStr, err)
		return err
	}
	return nil
}

// Check oss options
func (p *OssPlugin) checkOptions(opt *OssOptions) error {
	if opt.Url == "" || opt.Bucket == "" {
		return errors.New("Oss: Url or bucket is empty")
	}

	if opt.SecretAkId != "" && opt.SecretAkSec != "" {
		tmpId, err := base64.StdEncoding.DecodeString(opt.SecretAkId)
		if err != nil {
			return errors.New("Oss: SecretAkId decode error")
		}
		opt.AkId = string(tmpId)
		tmpSec, err := base64.StdEncoding.DecodeString(opt.SecretAkSec)
		if err != nil {
			return errors.New("Oss: SecretAkSec decode error")
		}
		opt.AkSecret = string(tmpSec)
	}
	// if not input ak from user, use the default ak value
	if opt.AkId == "" || opt.AkSecret == "" {
		opt.AkId, opt.AkSecret = utils.GetLocalAK()
	}

	if opt.OtherOpts != "" {
		if !strings.HasPrefix(opt.OtherOpts, "-o ") {
			return errors.New("Oss: OtherOpts format error: " + opt.OtherOpts)
		}
	}

	if opt.Uid == "" {
		opt.Uid = "0"
	} else {
		if _, err := strconv.Atoi(opt.Uid); err != nil {
			return errors.New("Oss: uid is not integer: " + opt.Uid)
		}
	}

	if opt.Gid == "" {
		opt.Gid = "0"
	} else {
		if _, err := strconv.Atoi(opt.Gid); err != nil {
			return errors.New("Oss: gid is not integer: " + opt.Gid)
		}
	}
	return nil
}

// getPodInfoFromMountDir parses pod information from the mountDir
func getPodInfoFromMountDir(mountDir string) (string, string, error) {
	// mountDir is in the form of <rootDir>/pods/<podID>/volumes/<flexvolume driver>/<volume name>
	filepath.Clean(mountDir)
	token := strings.Split(mountDir, string(filepath.Separator))
	// token length should at least size 5
	length := len(token)
	if length < 5 {
		return "", "", fmt.Errorf("failed to parse mountDir %s for CRD name and pod info", mountDir)
	}
	return token[length-4], token[length-1], nil
}
