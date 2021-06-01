/*
 * memfs.go
 *
 * Copyright 2017-2020 Bill Zissimopoulos
 */
/*
 * This file is part of Cgofuse.
 *
 * It is licensed under the MIT license. The full license text can be found
 * in the License.txt file at the root of this project.
 */

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/billziss-gh/cgofuse/examples/shared"
	"github.com/billziss-gh/cgofuse/fuse"
)

func trace(vals ...interface{}) func(vals ...interface{}) {
	uid, gid, _ := fuse.Getcontext()
	return shared.Trace(1, fmt.Sprintf("[uid=%v,gid=%v]", uid, gid), vals...)
}

func split(path string) []string {
	return strings.Split(path, "/")
}

func resize(slice []byte, size int64, zeroinit bool) []byte {
	const allocunit = 64 * 1024 // 分配
	allocsize := (size + allocunit - 1) / allocunit * allocunit
	if cap(slice) != int(allocsize) {
		var newslice []byte
		{
			defer func() {
				if r := recover(); nil != r {
					panic(fuse.Error(-fuse.ENOSPC))
				}
			}()
			newslice = make([]byte, size, allocsize)
		}
		copy(newslice, slice)
		slice = newslice
	} else if zeroinit {
		i := len(slice)
		slice = slice[:size]
		for ; len(slice) > i; i++ {
			slice[i] = 0
		}
	}
	return slice
}

type node_t struct {
	stat    fuse.Stat_t
	xatr    map[string][]byte
	chld    map[string]*node_t
	data    []byte
	opencnt int
}

func newNode(dev uint64, ino uint64, mode uint32, uid uint32, gid uint32) *node_t {
	tmsp := fuse.Now()
	// Stat_t 包含文件元数据信息。
	// 这个结构类似于 POSIX struct stat。
	// 并非所有 FUSE 实现都支持所有字段。
	self := node_t{
		fuse.Stat_t{
			Dev:      dev,  // 包含文件的设备的设备 ID。 [忽略]
			Ino:      ino,  // 文件序列号。 [忽略除非给出了 use_ino 挂载选项。]
			Mode:     mode, // 文件模式。
			Nlink:    1,    // 文件的硬链接数。
			Uid:      uid,  // 文件的用户 ID。
			Gid:      gid,  // 文件的组 ID。
			Atim:     tmsp, // 上次数据访问时间戳。
			Mtim:     tmsp, // 上次数据修改时间戳。
			Ctim:     tmsp, // 上次文件状态更改时间戳。
			Birthtim: tmsp, // 文件创建（出生）时间戳。 [仅限 OSX 和 Windows]
			Flags:    0,    // BSD 标志 (UF_*)。 [仅限 OSX 和 Windows]
		},
		nil,
		nil,
		nil,
		0}
	if fuse.S_IFDIR == self.stat.Mode&fuse.S_IFMT {
		self.chld = map[string]*node_t{}
	}
	return &self
}

type Memfs struct {
	fuse.FileSystemBase
	lock    sync.Mutex
	ino     uint64
	root    *node_t
	openmap map[uint64]*node_t // 打开的文件
}

// 创建一个文件节点。
func (self *Memfs) Mknod(path string, mode uint32, dev uint64) (errc int) {
	defer trace(path, mode, dev)(&errc)
	defer self.synchronize()()

	log.Printf("Mknod:  path: %s mode: %d dev: %d\n", path, mode, dev)
	return self.makeNode(path, mode, dev, nil)
}

// 创建一个目录。
func (self *Memfs) Mkdir(path string, mode uint32) (errc int) {
	defer trace(path, mode)(&errc)
	defer self.synchronize()()

	log.Printf("Mkdir:  path: %s mode: %d \n", path, mode)
	return self.makeNode(path, fuse.S_IFDIR|(mode&07777), 0, nil)
}

// 删除文件
func (self *Memfs) Unlink(path string) (errc int) {
	defer trace(path)(&errc)
	defer self.synchronize()()

	log.Printf("Unlink:  path: %s \n", path)
	return self.removeNode(path, false)
}

// 删除目录
func (self *Memfs) Rmdir(path string) (errc int) {
	defer trace(path)(&errc)
	defer self.synchronize()()

	log.Printf("Rmdir:  path: %s \n", path)
	return self.removeNode(path, true)
}

// 创建一个文件的硬链接
func (self *Memfs) Link(oldpath string, newpath string) (errc int) {
	defer trace(oldpath, newpath)(&errc)
	defer self.synchronize()()
	_, _, oldnode := self.lookupNode(oldpath, nil)
	if nil == oldnode {
		return -fuse.ENOENT
	}
	newprnt, newname, newnode := self.lookupNode(newpath, nil)
	if nil == newprnt {
		return -fuse.ENOENT
	}
	if nil != newnode {
		return -fuse.EEXIST
	}
	oldnode.stat.Nlink++
	newprnt.chld[newname] = oldnode
	tmsp := fuse.Now()
	oldnode.stat.Ctim = tmsp
	newprnt.stat.Ctim = tmsp
	newprnt.stat.Mtim = tmsp

	log.Printf("Link:  oldpath: %s newpath: %s\n", oldnode, newpath)

	return 0
}

// 创建一个软链接
func (self *Memfs) Symlink(target string, newpath string) (errc int) {
	defer trace(target, newpath)(&errc)
	defer self.synchronize()()

	log.Printf("Symlink:  target: %s newpath: %s\n", target, newpath)
	return self.makeNode(newpath, fuse.S_IFLNK|00777, 0, []byte(target))
}

// 读取软链接的目标
func (self *Memfs) Readlink(path string) (errc int, target string) {
	defer trace(path)(&errc, &target)
	defer self.synchronize()()
	_, _, node := self.lookupNode(path, nil)
	if nil == node {
		return -fuse.ENOENT, ""
	}
	if fuse.S_IFLNK != node.stat.Mode&fuse.S_IFMT {
		return -fuse.EINVAL, ""
	}

	log.Printf("Readlink:  path: %s \n", path)
	return 0, string(node.data)
}

// 重命名文件
func (self *Memfs) Rename(oldpath string, newpath string) (errc int) {
	defer trace(oldpath, newpath)(&errc)
	defer self.synchronize()()
	oldprnt, oldname, oldnode := self.lookupNode(oldpath, nil)
	if nil == oldnode {
		return -fuse.ENOENT
	}
	newprnt, newname, newnode := self.lookupNode(newpath, oldnode)
	if nil == newprnt {
		return -fuse.ENOENT
	}
	if "" == newname {
		// guard against directory loop creation
		return -fuse.EINVAL
	}
	if oldprnt == newprnt && oldname == newname {
		return 0
	}
	if nil != newnode {
		errc = self.removeNode(newpath, fuse.S_IFDIR == oldnode.stat.Mode&fuse.S_IFMT)
		if 0 != errc {
			return errc
		}
	}
	delete(oldprnt.chld, oldname)
	newprnt.chld[newname] = oldnode

	log.Printf("Rename:  oldpath: %s newpath: %s \n", oldpath, newpath)
	return 0
}

// 更改文件的权限位
func (self *Memfs) Chmod(path string, mode uint32) (errc int) {
	defer trace(path, mode)(&errc)
	defer self.synchronize()()
	_, _, node := self.lookupNode(path, nil)
	if nil == node {
		return -fuse.ENOENT
	}
	node.stat.Mode = (node.stat.Mode & fuse.S_IFMT) | mode&07777
	node.stat.Ctim = fuse.Now()

	log.Printf("Chmod:  path: %s mode: %d \n", path, mode)
	return 0
}

// 更改文件的所有者和组
func (self *Memfs) Chown(path string, uid uint32, gid uint32) (errc int) {
	defer trace(path, uid, gid)(&errc)
	defer self.synchronize()()
	_, _, node := self.lookupNode(path, nil)
	if nil == node {
		return -fuse.ENOENT
	}
	if ^uint32(0) != uid {
		node.stat.Uid = uid
	}
	if ^uint32(0) != gid {
		node.stat.Gid = gid
	}
	node.stat.Ctim = fuse.Now()

	log.Printf("Chmod:  path: %s uid: %d gid： %d \n", path, uid, gid)
	return 0
}

// 更改文件的访问和修改时间
func (self *Memfs) Utimens(path string, tmsp []fuse.Timespec) (errc int) {
	defer trace(path, tmsp)(&errc)
	defer self.synchronize()()
	_, _, node := self.lookupNode(path, nil)
	if nil == node {
		return -fuse.ENOENT
	}
	node.stat.Ctim = fuse.Now()
	if nil == tmsp {
		tmsp0 := node.stat.Ctim
		tmsa := [2]fuse.Timespec{tmsp0, tmsp0}
		tmsp = tmsa[:]
	}
	node.stat.Atim = tmsp[0]
	node.stat.Mtim = tmsp[1]

	marshal, _ := json.Marshal(tmsp)
	log.Printf("Utimens:  path: %s tmsp: %s \n", path, string(marshal))
	return 0
}

// 打开文件
func (self *Memfs) Open(path string, flags int) (errc int, fh uint64) {
	defer trace(path, flags)(&errc, &fh)
	defer self.synchronize()()

	log.Printf("Open:  path: %s flags: %d \n", path, flags)
	return self.openNode(path, false)
}

// 获取文件属性
func (self *Memfs) Getattr(path string, stat *fuse.Stat_t, fh uint64) (errc int) {
	defer trace(path, fh)(&errc, stat)
	defer self.synchronize()()
	node := self.getNode(path, fh)
	if nil == node {
		return -fuse.ENOENT
	}
	*stat = node.stat

	mp := ""
	if stat != nil {
		marshal, err := json.Marshal(*stat)
		if err == nil {
			mp = string(marshal)
		}
	}
	log.Printf("Getattr:  path: %s stat: %s fh: %d \n", path, mp, fh)
	return 0
}

// 更改文件的大小
func (self *Memfs) Truncate(path string, size int64, fh uint64) (errc int) {
	defer trace(path, size, fh)(&errc)
	defer self.synchronize()()
	node := self.getNode(path, fh)
	if nil == node {
		return -fuse.ENOENT
	}
	node.data = resize(node.data, size, true)
	node.stat.Size = size
	tmsp := fuse.Now()
	node.stat.Ctim = tmsp
	node.stat.Mtim = tmsp

	log.Printf("Truncate:  path: %s size: %d fh: %d \n", path, size, fh)
	return 0
}

// 从文件中读取数据
func (self *Memfs) Read(path string, buff []byte, ofst int64, fh uint64) (n int) {
	defer trace(path, buff, ofst, fh)(&n)
	defer self.synchronize()()
	node := self.getNode(path, fh)
	if nil == node {
		return -fuse.ENOENT
	}
	endofst := ofst + int64(len(buff))
	if endofst > node.stat.Size {
		endofst = node.stat.Size
	}
	if endofst < ofst {
		return 0
	}
	n = copy(buff, node.data[ofst:endofst])
	node.stat.Atim = fuse.Now()

	log.Printf("Read: path: %s ofst: %d fh: %d \n", path, ofst, fh)
	return
}

// 将数据写入文件
func (self *Memfs) Write(path string, buff []byte, ofst int64, fh uint64) (n int) {
	defer trace(path, buff, ofst, fh)(&n)
	defer self.synchronize()()
	node := self.getNode(path, fh)
	if nil == node {
		return -fuse.ENOENT
	}
	endofst := ofst + int64(len(buff))
	if endofst > node.stat.Size {
		node.data = resize(node.data, endofst, true)
		node.stat.Size = endofst
	}
	n = copy(node.data[ofst:endofst], buff)
	tmsp := fuse.Now()
	node.stat.Ctim = tmsp
	node.stat.Mtim = tmsp

	log.Printf("Write: path: %s ofst: %d fh: %d \n", path, ofst, fh)
	return
}

// 关闭一个打开的文件
func (self *Memfs) Release(path string, fh uint64) (errc int) {
	defer trace(path, fh)(&errc)
	defer self.synchronize()()

	log.Printf("Release: path: %s fh: %d \n", path, fh)
	return self.closeNode(fh)
}

//  打开一个目录
func (self *Memfs) Opendir(path string) (errc int, fh uint64) {
	defer trace(path)(&errc, &fh)
	defer self.synchronize()()

	log.Printf("Opendir: path: %s \n", path)
	return self.openNode(path, true)
}

// 读取一个目录
func (self *Memfs) Readdir(path string,
	fill func(name string, stat *fuse.Stat_t, ofst int64) bool,
	ofst int64,
	fh uint64) (errc int) {
	defer trace(path, fill, ofst, fh)(&errc)
	defer self.synchronize()()
	node := self.openmap[fh]
	fill(".", &node.stat, 0)
	fill("..", nil, 0)
	for name, chld := range node.chld {
		if !fill(name, &chld.stat, 0) {
			break
		}
	}

	log.Printf("Readdir: path: %s ofst: %d fh: %d \n", path, ofst, fh)
	return 0
}

// 关闭一个打开的目录
func (self *Memfs) Releasedir(path string, fh uint64) (errc int) {
	defer trace(path, fh)(&errc)
	defer self.synchronize()()

	log.Printf("Releasedir: path: %s fh: %d \n", path, fh)
	return self.closeNode(fh)
}

// 设置扩展属性
func (self *Memfs) Setxattr(path string, name string, value []byte, flags int) (errc int) {
	defer trace(path, name, value, flags)(&errc)
	defer self.synchronize()()
	_, _, node := self.lookupNode(path, nil)
	if nil == node {
		return -fuse.ENOENT
	}
	if "com.apple.ResourceFork" == name {
		return -fuse.ENOTSUP
	}
	if fuse.XATTR_CREATE == flags {
		if _, ok := node.xatr[name]; ok {
			return -fuse.EEXIST
		}
	} else if fuse.XATTR_REPLACE == flags {
		if _, ok := node.xatr[name]; !ok {
			return -fuse.ENOATTR
		}
	}
	xatr := make([]byte, len(value))
	copy(xatr, value)
	if nil == node.xatr {
		node.xatr = map[string][]byte{}
	}
	node.xatr[name] = xatr

	log.Printf("Setxattr: path: %s name: %s flags: %d \n", path, name, flags)
	return 0
}

// 获取扩展属性
func (self *Memfs) Getxattr(path string, name string) (errc int, xatr []byte) {
	defer trace(path, name)(&errc, &xatr)
	defer self.synchronize()()
	_, _, node := self.lookupNode(path, nil)
	if nil == node {
		return -fuse.ENOENT, nil
	}
	if "com.apple.ResourceFork" == name {
		return -fuse.ENOTSUP, nil
	}
	xatr, ok := node.xatr[name]
	if !ok {
		return -fuse.ENOATTR, nil
	}

	log.Printf("Getxattr: path: %s name: %s \n", path, name)
	return 0, xatr
}

// 删除扩展属性
func (self *Memfs) Removexattr(path string, name string) (errc int) {
	defer trace(path, name)(&errc)
	defer self.synchronize()()
	_, _, node := self.lookupNode(path, nil)
	if nil == node {
		return -fuse.ENOENT
	}
	if "com.apple.ResourceFork" == name {
		return -fuse.ENOTSUP
	}
	if _, ok := node.xatr[name]; !ok {
		return -fuse.ENOATTR
	}
	delete(node.xatr, name)


	log.Printf("Removexattr: path: %s name: %s \n", path, name)
	return 0
}

// 列出扩展属性
func (self *Memfs) Listxattr(path string, fill func(name string) bool) (errc int) {
	defer trace(path, fill)(&errc)
	defer self.synchronize()()
	_, _, node := self.lookupNode(path, nil)
	if nil == node {
		return -fuse.ENOENT
	}
	for name := range node.xatr {
		if !fill(name) {
			return -fuse.ERANGE
		}
	}

	log.Printf("Listxattr: path: %s \n", path)
	return 0
}

// filesystemchflags是包装chflags方法的接口。
//
// chflags更改BSD文件标志（Windows文件属性）。 [osx和windows]
func (self *Memfs) Chflags(path string, flags uint32) (errc int) {
	defer trace(path, flags)(&errc)
	defer self.synchronize()()
	_, _, node := self.lookupNode(path, nil)
	if nil == node {
		return -fuse.ENOENT
	}
	node.stat.Flags = flags
	node.stat.Ctim = fuse.Now()


	log.Printf("Chflags: path: %s flags: %d \n", path, flags)
	return 0
}

// filesystemsetcrtime是包装setcrtime方法的接口。
//
// setcrtime更改文件创建（出生）时间。 [osx和windows]
func (self *Memfs) Setcrtime(path string, tmsp fuse.Timespec) (errc int) {
	defer trace(path, tmsp)(&errc)
	defer self.synchronize()()
	_, _, node := self.lookupNode(path, nil)
	if nil == node {
		return -fuse.ENOENT
	}
	node.stat.Birthtim = tmsp
	node.stat.Ctim = fuse.Now()

	log.Printf("Setcrtime: path: %s tmsp: %d \n", path, tmsp.Sec)
	return 0
}

// filesystemsetchgtime是包装SetChgTime方法的接口。
//
// SetChgTime更改文件更改（立方）时间。 [osx和windows]
func (self *Memfs) Setchgtime(path string, tmsp fuse.Timespec) (errc int) {
	defer trace(path, tmsp)(&errc)
	defer self.synchronize()()
	_, _, node := self.lookupNode(path, nil)
	if nil == node {
		return -fuse.ENOENT
	}
	node.stat.Ctim = tmsp

	log.Printf("Setchgtime: path: %s tmsp: %d \n", path, tmsp.Sec)
	return 0
}

func (self *Memfs) lookupNode(path string, ancestor *node_t) (prnt *node_t, name string, node *node_t) {
	prnt = self.root
	name = ""
	node = self.root
	for _, c := range split(path) {
		if "" != c {
			if 255 < len(c) {
				panic(fuse.Error(-fuse.ENAMETOOLONG))
			}
			prnt, name = node, c
			if node == nil {
				return
			}
			node = node.chld[c]
			if nil != ancestor && node == ancestor {
				name = "" // special case loop condition
				return
			}
		}
	}
	return
}

func (self *Memfs) makeNode(path string, mode uint32, dev uint64, data []byte) int {
	prnt, name, node := self.lookupNode(path, nil)
	if nil == prnt {
		return -fuse.ENOENT
	}
	if nil != node {
		return -fuse.EEXIST
	}
	self.ino++
	uid, gid, _ := fuse.Getcontext()
	node = newNode(dev, self.ino, mode, uid, gid)
	if nil != data {
		node.data = make([]byte, len(data))
		node.stat.Size = int64(len(data))
		copy(node.data, data)
	}
	prnt.chld[name] = node
	prnt.stat.Ctim = node.stat.Ctim
	prnt.stat.Mtim = node.stat.Ctim
	return 0
}

func (self *Memfs) removeNode(path string, dir bool) int {
	prnt, name, node := self.lookupNode(path, nil)
	if nil == node {
		return -fuse.ENOENT
	}
	if !dir && fuse.S_IFDIR == node.stat.Mode&fuse.S_IFMT {
		return -fuse.EISDIR
	}
	if dir && fuse.S_IFDIR != node.stat.Mode&fuse.S_IFMT {
		return -fuse.ENOTDIR
	}
	if 0 < len(node.chld) {
		return -fuse.ENOTEMPTY
	}
	node.stat.Nlink--
	delete(prnt.chld, name)
	tmsp := fuse.Now()
	node.stat.Ctim = tmsp
	prnt.stat.Ctim = tmsp
	prnt.stat.Mtim = tmsp
	return 0
}

func (self *Memfs) openNode(path string, dir bool) (int, uint64) {
	_, _, node := self.lookupNode(path, nil)
	if nil == node {
		return -fuse.ENOENT, ^uint64(0)
	}
	if !dir && fuse.S_IFDIR == node.stat.Mode&fuse.S_IFMT {
		return -fuse.EISDIR, ^uint64(0)
	}
	if dir && fuse.S_IFDIR != node.stat.Mode&fuse.S_IFMT {
		return -fuse.ENOTDIR, ^uint64(0)
	}
	node.opencnt++
	if 1 == node.opencnt {
		self.openmap[node.stat.Ino] = node
	}
	return 0, node.stat.Ino
}

func (self *Memfs) closeNode(fh uint64) int {
	node := self.openmap[fh]
	node.opencnt--
	if 0 == node.opencnt {
		delete(self.openmap, node.stat.Ino)
	}
	return 0
}

func (self *Memfs) getNode(path string, fh uint64) *node_t {
	if ^uint64(0) == fh {
		_, _, node := self.lookupNode(path, nil)
		return node
	} else {
		return self.openmap[fh]
	}
}

func (self *Memfs) synchronize() func() {
	self.lock.Lock()
	return func() {
		self.lock.Unlock()
	}
}

func NewMemfs() *Memfs {
	self := Memfs{}
	defer self.synchronize()()
	self.ino++
	self.root = newNode(0, self.ino, fuse.S_IFDIR|00777, 0, 0)
	self.openmap = map[uint64]*node_t{}
	return &self
}

var _ fuse.FileSystemChflags = (*Memfs)(nil)
var _ fuse.FileSystemSetcrtime = (*Memfs)(nil)
var _ fuse.FileSystemSetchgtime = (*Memfs)(nil)

func main() {
	log.SetFlags(log.Llongfile | log.LstdFlags)

	memfs := NewMemfs()
	host := fuse.NewFileSystemHost(memfs)
	host.SetCapReaddirPlus(true)
	host.Mount("", os.Args[1:])
}
