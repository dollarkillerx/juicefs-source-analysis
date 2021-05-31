# juicefs-source-analysis
juicefs source analysis 

以`minio`为底层存储 对 `juicefs` 进行源码分析

### 统一存储接口

```go
// ObjectStorage is the interface for object storage.
// all of these API should be idempotent.
// objectStorage是对象存储的接口。
// 所有这些API都应是幂等的。
type ObjectStorage interface {
	// Description of the object storage.
    // 对象存储的描述。
	String() string
	// Create the bucket if not existed.
    // 如果不存在，请创建存储桶。
	Create() error
	// Get the data for the given object specified by key.
    // 获取按键指定的给定对象的数据。
	Get(key string, off, limit int64) (io.ReadCloser, error)
	// Put data read from a reader to an object specified by key.
    // 将数据从读取器读取到按键指定的对象。
	Put(key string, in io.Reader) error
	// Delete a object.
    // 删除对象。
	Delete(key string) error

	// Head returns some information about the object or an error if not found.
    // head返回有关对象的一些信息或如果找不到错误。
	Head(key string) (Object, error)
	// List returns a list of objects.
    // 列表返回对象列表。
	List(prefix, marker string, limit int64) ([]Object, error)
	// ListAll returns all the objects as an channel.
    // ListAll将所有对象返回为频道。
	ListAll(prefix, marker string) (<-chan Object, error)

	// CreateMultipartUpload starts to upload a large object part by part.
    // CreateMultIpArtupload开始按零部键上载一个大对象部分。
	CreateMultipartUpload(key string) (*MultipartUpload, error)
	// UploadPart upload a part of an object.
    // uploadpart上传对象的一部分。
	UploadPart(key string, uploadID string, num int, body []byte) (*Part, error)
	// AbortUpload abort a multipart upload.
    // abortupload中止一个多重上传。
	AbortUpload(key string, uploadID string)
	// CompleteUpload finish an multipart upload.
    // 完全完成多重上传。
	CompleteUpload(key string, uploadID string, parts []*Part) error
	// ListUploads lists existing multipart uploads.
    // ListUploads列出现有的MultiPart上传。
	ListUploads(marker string) ([]*PendingPart, string, error)
}
```

#### minio 对 上述interface实现

```go
type minio struct {
	s3client
}

// minio description
func (m *minio) String() string {
	return *m.s3client.ses.Config.Endpoint
}

// create 
func (m *minio) Create() error {
	return m.s3client.Create()
}
```

juicefs 对于 minio 复用了 aws s3的实现

```go
const awsDefaultRegion = "us-east-1"

type s3client struct {
	bucket string
	s3     *s3.S3
	ses    *session.Session
}

func (s *s3client) String() string {
	return fmt.Sprintf("s3://%s", s.bucket)
}

func (s *s3client) Create() error {
	_, err := s.s3.CreateBucket(&s3.CreateBucketInput{Bucket: &s.bucket})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case s3.ErrCodeBucketAlreadyExists:
				err = nil
			case s3.ErrCodeBucketAlreadyOwnedByYou:
				err = nil
			}
		}
	}
	return err
}

func (s *s3client) Head(key string) (Object, error) {
	param := s3.HeadObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	}
	r, err := s.s3.HeadObject(&param)
	if err != nil {
		return nil, err
	}
	return &obj{
		key,
		*r.ContentLength,
		*r.LastModified,
		strings.HasSuffix(key, "/"),
	}, nil
}

func (s *s3client) Get(key string, off, limit int64) (io.ReadCloser, error) {
	params := &s3.GetObjectInput{Bucket: &s.bucket, Key: &key}
	if off > 0 || limit > 0 {
		var r string
		if limit > 0 {
			r = fmt.Sprintf("bytes=%d-%d", off, off+limit-1)
		} else {
			r = fmt.Sprintf("bytes=%d-", off)
		}
		params.Range = &r
	}
	resp, err := s.s3.GetObject(params)
	if err != nil {
		return nil, err
	}
	if off == 0 && limit == -1 {
		cs := resp.Metadata[checksumAlgr]
		if cs != nil {
			resp.Body = verifyChecksum(resp.Body, *cs)
		}
	}
	return resp.Body, nil
}

func (s *s3client) Put(key string, in io.Reader) error {
	var body io.ReadSeeker
	if b, ok := in.(io.ReadSeeker); ok {
		body = b
	} else {
		data, err := ioutil.ReadAll(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	checksum := generateChecksum(body)
	params := &s3.PutObjectInput{
		Bucket:   &s.bucket,
		Key:      &key,
		Body:     body,
		Metadata: map[string]*string{checksumAlgr: &checksum},
	}
	_, err := s.s3.PutObject(params)
	return err
}

func (s *s3client) Copy(dst, src string) error {
	src = s.bucket + "/" + src
	params := &s3.CopyObjectInput{
		Bucket:     &s.bucket,
		Key:        &dst,
		CopySource: &src,
	}
	_, err := s.s3.CopyObject(params)
	return err
}

func (s *s3client) Delete(key string) error {
	param := s3.DeleteObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	}
	_, err := s.s3.DeleteObject(&param)
	return err
}

func (s *s3client) List(prefix, marker string, limit int64) ([]Object, error) {
	param := s3.ListObjectsInput{
		Bucket:  &s.bucket,
		Prefix:  &prefix,
		Marker:  &marker,
		MaxKeys: &limit,
	}
	resp, err := s.s3.ListObjects(&param)
	if err != nil {
		return nil, err
	}
	n := len(resp.Contents)
	objs := make([]Object, n)
	for i := 0; i < n; i++ {
		o := resp.Contents[i]
		objs[i] = &obj{
			*o.Key,
			*o.Size,
			*o.LastModified,
			strings.HasSuffix(*o.Key, "/"),
		}
	}
	return objs, nil
}

func (s *s3client) ListAll(prefix, marker string) (<-chan Object, error) {
	return nil, notSupported
}

func (s *s3client) CreateMultipartUpload(key string) (*MultipartUpload, error) {
	params := &s3.CreateMultipartUploadInput{
		Bucket: &s.bucket,
		Key:    &key,
	}
	resp, err := s.s3.CreateMultipartUpload(params)
	if err != nil {
		return nil, err
	}
	return &MultipartUpload{UploadID: *resp.UploadId, MinPartSize: 5 << 20, MaxCount: 10000}, nil
}

func (s *s3client) UploadPart(key string, uploadID string, num int, body []byte) (*Part, error) {
	n := int64(num)
	params := &s3.UploadPartInput{
		Bucket:     &s.bucket,
		Key:        &key,
		UploadId:   &uploadID,
		Body:       bytes.NewReader(body),
		PartNumber: &n,
	}
	resp, err := s.s3.UploadPart(params)
	if err != nil {
		return nil, err
	}
	return &Part{Num: num, ETag: *resp.ETag}, nil
}

func (s *s3client) AbortUpload(key string, uploadID string) {
	params := &s3.AbortMultipartUploadInput{
		Bucket:   &s.bucket,
		Key:      &key,
		UploadId: &uploadID,
	}
	_, _ = s.s3.AbortMultipartUpload(params)
}

func (s *s3client) CompleteUpload(key string, uploadID string, parts []*Part) error {
	var s3Parts []*s3.CompletedPart
	for i := range parts {
		n := new(int64)
		*n = int64(parts[i].Num)
		s3Parts = append(s3Parts, &s3.CompletedPart{ETag: &parts[i].ETag, PartNumber: n})
	}
	params := &s3.CompleteMultipartUploadInput{
		Bucket:          &s.bucket,
		Key:             &key,
		UploadId:        &uploadID,
		MultipartUpload: &s3.CompletedMultipartUpload{Parts: s3Parts},
	}
	_, err := s.s3.CompleteMultipartUpload(params)
	return err
}

func (s *s3client) ListUploads(marker string) ([]*PendingPart, string, error) {
	input := &s3.ListMultipartUploadsInput{
		Bucket:    aws.String(s.bucket),
		KeyMarker: aws.String(marker),
	}

	result, err := s.s3.ListMultipartUploads(input)
	if err != nil {
		return nil, "", err
	}
	parts := make([]*PendingPart, len(result.Uploads))
	for i, u := range result.Uploads {
		parts[i] = &PendingPart{*u.Key, *u.UploadId, *u.Initiated}
	}
	var nextMarker string
	if result.NextKeyMarker != nil {
		nextMarker = *result.NextKeyMarker
	}
	return parts, nextMarker, nil
}
```

### GO开源fuse 比较
- github.com/billziss-gh/cgofuse 兼容几乎所有操作系统  (juicefs将此作为win挂载)
- github.com/bazil/fuse  仅兼容linux
- github.com/hanwen/go-fuse/  (juicefs将此作为linux挂载)



``` 
// FileSystemBase provides default implementations of the methods in FileSystemInterface.
// The default implementations are either empty or return -ENOSYS to signal that the
// file system does not implement a particular operation to the FUSE layer.
// filesystembase提供fileSystemInterface中的方法的默认实现。
//默认实现为空或返回 - 以发出信号
//文件系统未对保险丝层实现特定操作。
type FileSystemBase struct {
}

// Init is called when the file system is created.
// The FileSystemBase implementation does nothing.
//在创建文件系统时调用init。
// filesystembase实现没有任何内容。
func (*FileSystemBase) Init() {
}

// Destroy is called when the file system is destroyed.
// The FileSystemBase implementation does nothing.
//在文件系统被销毁时调用销毁。
// filesystembase实现没有任何内容。
func (*FileSystemBase) Destroy() {
}

// Statfs gets file system statistics.
// The FileSystemBase implementation returns -ENOSYS.
// statfs获取文件系统统计信息。
// filesystembase实现返回-enosys。
func (*FileSystemBase) Statfs(path string, stat *Statfs_t) int {
	return -ENOSYS
}

// Mknod creates a file node.
// The FileSystemBase implementation returns -ENOSYS.
// mknod创建一个文件节点。
// filesystembase实现返回-enosys。
func (*FileSystemBase) Mknod(path string, mode uint32, dev uint64) int {
	return -ENOSYS
}

// Mkdir creates a directory.
// The FileSystemBase implementation returns -ENOSYS.
// mkdir创建一个目录。
// filesystembase实现返回-enosys。
func (*FileSystemBase) Mkdir(path string, mode uint32) int {
	return -ENOSYS
}

// Unlink removes a file.
// The FileSystemBase implementation returns -ENOSYS.
//  Unlink 删除文件。
// filesystembase实现返回-enosys。
func (*FileSystemBase) Unlink(path string) int {
	return -ENOSYS
}

// Rmdir removes a directory. 删除目录。
// The FileSystemBase implementation returns -ENOSYS. FileSystemBase实现返回-enosys。
func (*FileSystemBase) Rmdir(path string) int {
	return -ENOSYS
}

// Link creates a hard link to a file. 创建一个文件的硬链接。
// The FileSystemBase implementation returns -ENOSYS. FileSystemBase实现返回-enosys。
func (*FileSystemBase) Link(oldpath string, newpath string) int {
	return -ENOSYS
}

// Symlink creates a symbolic link. 创建一个软链接。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Symlink(target string, newpath string) int {
	return -ENOSYS
}

// Readlink reads the target of a symbolic link. 读取软链接的目标。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Readlink(path string) (int, string) {
	return -ENOSYS, ""
}

// Rename renames a file. 重命名文件。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Rename(oldpath string, newpath string) int {
	return -ENOSYS
}

// Chmod changes the permission bits of a file. 更改文件的权限位。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Chmod(path string, mode uint32) int {
	return -ENOSYS
}

// Chown changes the owner and group of a file. 更改文件的所有者和组。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Chown(path string, uid uint32, gid uint32) int {
	return -ENOSYS
}

// Utimens changes the access and modification times of a file. 更改文件的访问和修改时间。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Utimens(path string, tmsp []Timespec) int {
	return -ENOSYS
}
 
// Access checks file access permissions. 检查文件访问权限。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Access(path string, mask uint32) int {
	return -ENOSYS
}

// Create creates and opens a file. 创建并打开文件。
// The flags are a combination of the fuse.O_* constants.
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Create(path string, flags int, mode uint32) (int, uint64) {
	return -ENOSYS, ^uint64(0)
}

// Open opens a file. 打开文件。
// The flags are a combination of the fuse.O_* constants.
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Open(path string, flags int) (int, uint64) {
	return -ENOSYS, ^uint64(0)
}

// Getattr gets file attributes. 获取文件属性。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Getattr(path string, stat *Stat_t, fh uint64) int {
	return -ENOSYS
}

// Truncate changes the size of a file. 更改文件的大小。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Truncate(path string, size int64, fh uint64) int {
	return -ENOSYS
}

// Read reads data from a file. 从文件中读取数据。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Read(path string, buff []byte, ofst int64, fh uint64) int {
	return -ENOSYS
}

// Write writes data to a file. 将数据写入文件。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Write(path string, buff []byte, ofst int64, fh uint64) int {
	return -ENOSYS
}

// Flush flushes cached file data. 刷新缓存的文件数据。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Flush(path string, fh uint64) int {
	return -ENOSYS
}

// Release closes an open file. 关闭一个打开的文件。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Release(path string, fh uint64) int {
	return -ENOSYS
}

// Fsync synchronizes file contents. 同步文件内容。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Fsync(path string, datasync bool, fh uint64) int {
	return -ENOSYS
}

/*
// Lock performs a file locking operation. 执行文件锁定操作。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Lock(path string, cmd int, lock *Lock_t, fh uint64) int {
	return -ENOSYS
}
*/

// Opendir opens a directory. 打开一个目录。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Opendir(path string) (int, uint64) {
	return -ENOSYS, ^uint64(0)
}

// Readdir reads a directory. 读取一个目录。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Readdir(path string,
	fill func(name string, stat *Stat_t, ofst int64) bool,
	ofst int64,
	fh uint64) int {
	return -ENOSYS
}

// Releasedir closes an open directory. 关闭一个打开的目录。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Releasedir(path string, fh uint64) int {
	return -ENOSYS
}

// Fsyncdir synchronizes directory contents. 同步目录内容。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Fsyncdir(path string, datasync bool, fh uint64) int {
	return -ENOSYS
}

// Setxattr sets extended attributes. 设置扩展属性。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Setxattr(path string, name string, value []byte, flags int) int {
	return -ENOSYS
}

// Getxattr gets extended attributes. 获取扩展属性。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Getxattr(path string, name string) (int, []byte) {
	return -ENOSYS, nil
}

// Removexattr removes extended attributes. 删除扩展属性。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Removexattr(path string, name string) int {
	return -ENOSYS
}

// Listxattr lists extended attributes. 列出扩展属性。
// The FileSystemBase implementation returns -ENOSYS.
func (*FileSystemBase) Listxattr(path string, fill func(name string) bool) int {
	return -ENOSYS
}
```