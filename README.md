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