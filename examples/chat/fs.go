// Code generated by "esc -o fs.go -prefix www www"; DO NOT EDIT.

package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"sync"
	"time"
)

type _escLocalFS struct{}

var _escLocal _escLocalFS

type _escStaticFS struct{}

var _escStatic _escStaticFS

type _escDirectory struct {
	fs   http.FileSystem
	name string
}

type _escFile struct {
	compressed string
	size       int64
	modtime    int64
	local      string
	isDir      bool

	once sync.Once
	data []byte
	name string
}

func (_escLocalFS) Open(name string) (http.File, error) {
	f, present := _escData[path.Clean(name)]
	if !present {
		return nil, os.ErrNotExist
	}
	return os.Open(f.local)
}

func (_escStaticFS) prepare(name string) (*_escFile, error) {
	f, present := _escData[path.Clean(name)]
	if !present {
		return nil, os.ErrNotExist
	}
	var err error
	f.once.Do(func() {
		f.name = path.Base(name)
		if f.size == 0 {
			return
		}
		var gr *gzip.Reader
		b64 := base64.NewDecoder(base64.StdEncoding, bytes.NewBufferString(f.compressed))
		gr, err = gzip.NewReader(b64)
		if err != nil {
			return
		}
		f.data, err = ioutil.ReadAll(gr)
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (fs _escStaticFS) Open(name string) (http.File, error) {
	f, err := fs.prepare(name)
	if err != nil {
		return nil, err
	}
	return f.File()
}

func (dir _escDirectory) Open(name string) (http.File, error) {
	return dir.fs.Open(dir.name + name)
}

func (f *_escFile) File() (http.File, error) {
	type httpFile struct {
		*bytes.Reader
		*_escFile
	}
	return &httpFile{
		Reader:   bytes.NewReader(f.data),
		_escFile: f,
	}, nil
}

func (f *_escFile) Close() error {
	return nil
}

func (f *_escFile) Readdir(count int) ([]os.FileInfo, error) {
	if !f.isDir {
		return nil, fmt.Errorf(" escFile.Readdir: '%s' is not directory", f.name)
	}

	fis, ok := _escDirs[f.local]
	if !ok {
		return nil, fmt.Errorf(" escFile.Readdir: '%s' is directory, but we have no info about content of this dir, local=%s", f.name, f.local)
	}
	limit := count
	if count <= 0 || limit > len(fis) {
		limit = len(fis)
	}

	if len(fis) == 0 && count > 0 {
		return nil, io.EOF
	}

	return fis[0:limit], nil
}

func (f *_escFile) Stat() (os.FileInfo, error) {
	return f, nil
}

func (f *_escFile) Name() string {
	return f.name
}

func (f *_escFile) Size() int64 {
	return f.size
}

func (f *_escFile) Mode() os.FileMode {
	return 0
}

func (f *_escFile) ModTime() time.Time {
	return time.Unix(f.modtime, 0)
}

func (f *_escFile) IsDir() bool {
	return f.isDir
}

func (f *_escFile) Sys() interface{} {
	return f
}

// FS returns a http.Filesystem for the embedded assets. If useLocal is true,
// the filesystem's contents are instead used.
func FS(useLocal bool) http.FileSystem {
	if useLocal {
		return _escLocal
	}
	return _escStatic
}

// Dir returns a http.Filesystem for the embedded assets on a given prefix dir.
// If useLocal is true, the filesystem's contents are instead used.
func Dir(useLocal bool, name string) http.FileSystem {
	if useLocal {
		return _escDirectory{fs: _escLocal, name: name}
	}
	return _escDirectory{fs: _escStatic, name: name}
}

// FSByte returns the named file from the embedded assets. If useLocal is
// true, the filesystem's contents are instead used.
func FSByte(useLocal bool, name string) ([]byte, error) {
	if useLocal {
		f, err := _escLocal.Open(name)
		if err != nil {
			return nil, err
		}
		b, err := ioutil.ReadAll(f)
		_ = f.Close()
		return b, err
	}
	f, err := _escStatic.prepare(name)
	if err != nil {
		return nil, err
	}
	return f.data, nil
}

// FSMustByte is the same as FSByte, but panics if name is not present.
func FSMustByte(useLocal bool, name string) []byte {
	b, err := FSByte(useLocal, name)
	if err != nil {
		panic(err)
	}
	return b
}

// FSString is the string version of FSByte.
func FSString(useLocal bool, name string) (string, error) {
	b, err := FSByte(useLocal, name)
	return string(b), err
}

// FSMustString is the string version of FSMustByte.
func FSMustString(useLocal bool, name string) string {
	return string(FSMustByte(useLocal, name))
}

var _escData = map[string]*_escFile{

	"/chat.css": {
		name:    "chat.css",
		local:   "www/chat.css",
		size:    672,
		modtime: 1568056197,
		compressed: `
H4sIAAAAAAAC/4SRQU+7QBDF7/0Uk5L/EQL8tcHtzTZGE409GBOPtDuFiTC72V0sxvjdDRRIQRJvm9k3
v5k37/7l6RG+FgAAB1UoI8CL43jdFvbp4T0zqmIp4JSTw/Xie3H7vH3rGsrUZMQCwk6ujEQjgBXjuaJT
KYkzAdFK13Cl6+6R6LpBbR9e50lDX9jIdhNRw2k/vBKtTTO0sAssmg80EydJkox1gaMSpzPPQPD/r5q1
xvObvzFCB+WnxeI4Yl1eyrvbbC4P4ptUUmXFb1RgkeWw9FGx809IWe4E7FUhZ8UiPboLn+yQnYClgGUr
J9aVmxiM4tZfCP5Nb7CPKtI1WFWQBG/Tbz2Yv9Y1dEl13Dn63+F3ihNJlwuIwvDfuaAqVxBj3zMcQQBx
joZcM/knAAD//9ZT6qWgAgAA
`,
	},

	"/chat.js": {
		name:    "chat.js",
		local:   "www/chat.js",
		size:    2340,
		modtime: 1568054952,
		compressed: `
H4sIAAAAAAAC/4xVUW/bOAx+z6/gPcm5Di7avjUwDl3bYTt0vaHpYQ/DcFBs2hYii4IkJ/Ft+e+DLDlO
lqXdkymR+vTxIymvuIG85g6y8Pn+Hb5tZ5OJX6TvPjzNn/97vPl4P4cMvkwAGDeGKq4ce+NXOamytVjE
VeswWAVXuVBVWJQCTR4dpWzLsgt2ZTBadafJ1ehEzmXYkUJVaHYQkoTbWyouVhFQC7UMlhWq2kVYRfT/
uHKGqwrZ5OuQ2cPNUWIbQU0IX1DktWiV6sDwhRjzdYYXIneCVNwylFMh5JC5QR5hEGMylTDRqoXWpMnx
prVhh9bRRZuuITOg6lpIsqRrNGHDUD7kKcnVe6nkpBTmvoBlq3piyRS+TQB8abUhR5BBIinn3pf2OzlJ
yDJgtXPaXjP4C9jaeuPaG9dsOoG+HdJ/nx4giyhnwM7PGZzBDqsm6/ptrsW5j2ez4eDaQgYK1/AZF3PK
l+iSAXA6m0R2pTB26Lz9Zvvykbs6LSWRSXrTcFVQk0zhz6PYVKKqXD39Oougko+YY5lfhRxDjxAVb9DL
27M9Awa9CNy6PpGYbsqL4n6Fyj0I61ChSRhpVOzNWBb07lCb8ZhFVSTs9v3Ncw/r75r6i7ejTA1ayyu0
d2IFGRSUtw0ql1bo7iV68233oUjYEMamsxdpxbiTzPorbQUZ/D3/5zHV3FgMEWnBHQ+8Ynvt88kNcoeR
UsI+BR4AooSksVX6zlDTdx0bbgLQaS65tY9BYWbRrNCwcG4LKC0eHe8VOgXQdBZlOQCMRO3FC0znn24e
B7JFbNs77rC/9nONKvrsRSqUQvOMG99hRerogXIu8Vk0OHf+fUrG0ANeTjQYWemUa42quK2FLBJ7Meh5
kOcfhzL1KVz+ZgoA9vKA6IC65/5JdVXsVD8ieDk9VvPqN6nYqyMi3tw5DzXCjfu1RleDRnuTcBChfx4Z
n9Jb2rw0Lj5kQZtA9fwc7jfof2DAd2MB6xoVuBqhtWjAoERu0QKHJXZAwbXEbkHcFBMYLv3FyC2xK2h9
+jHwtQ8TtsTulgqELMvg4mrsgODVpv/eYclb6ZJdwX3KLmg8kFhx2eLg9/jBf9hXAAZda9QQt43fg8fJ
Hxxbax/eV203awf6C6Vb98p71ce89ljlkuzpp2q4JrWuk5gWwmrJO09LkfID92LXnOrg909sumuo7Wzy
IwAA///DMplFJAkAAA==
`,
	},

	"/favicon.ico": {
		name:    "favicon.ico",
		local:   "www/favicon.ico",
		size:    4286,
		modtime: 1567447566,
		compressed: `
H4sIAAAAAAAC/8SX2XMc13XG4f/AT3nWY/6MPOQhqSylqiyVOHElrliushWbsaRYlOg4EkiRAiHIJECQ
AIiFWIl9mRWzYfatp7unZ+npmenZ98HMYOEGkaLyS2n8mD8g5+VW3bp1v3vvd853vjsy8oORH4y88873
4zsjmz8cGfmjkZGRPx4ZGXlnZGTkT0b+MD+MH478nxh7MM/6gZGgkkRKpXH4w2wf+1jdMyKkUiztGfhq
dg1HWCAQi6OXS2j5AjaHA7fPi5TPozZaeONJ/LKMV5SJJNKo+TzRmIDT4+HAbEVSVVa29gmEI7jcXtyh
IEf2EyYeLbBtPkbUcnhiEhZfhCNfjPVDCx5JYX7Pwuz6Aa6IQFBOUaw3KFSruHx+/NEowVQGKVfk0Gxn
z3zMyr6JWDJDoV5HVOKIisJJKEKpUefA6iKV1wmKMmJaxR0WGZua4cjpRs4VMHmCHLqCHLoFts1OPEqK
+W0DBm8MW1jGGpJQSlUkTUNIJNFyOZRsDiWTw+l0srlv4Nont0nldJqNOs1mDTWfZW17n0bvlKcmBwGt
zP2nVj6d2uCXo9NMPn6C8cSLlNGx+aPs2n1sHvvZNDlwRES2LG6W9ixYAyLOaJxEuUZGLxKOK0RlhVha
RUxn8Ac9rO0ece2TL0nmdOr1Go1GjVwuh9MboN7tc+gMcCLnuLti5drYGu/dmGLy8SKeiIBeqZDJadh8
QdYMLvbNDvyCgDMk8/XcKod2Dz4hRqnZRNd17Cd+tkwObo5P8/XMEgvrWzxcN/HhZ79HUrNouk5KyxAU
5OF+RneMFYMTIVNg2ehjav2Ysce7LKw9JZ5OU2/WqFeL+CMCj7dMbBwYUPU8wUSKI0+YUEwkrar0Bj16
/R4Gu5fZLSt/86//zt/99D/44tEKX62a+fXoFHFVp1Auk9Cy+MQE18aW+fnoPDdnN8nWWgSULEKmiNEb
56vJh4TFOLmCTqlcxB0WuHn/MRMzixzaXEzMLGDyCYSEGLIcp91p0h/02DHa+f3iFj967xo/+cWHfH5v
jqnNY754uEFKL1OpVcjqJfwRmduLRj5+sMXEsgExV8Xij+OR0uw4ovx29A6eWJxkoUi+VsUjSIzPLvPu
j/6Fa9dv8Gfv/j0r+8d4gmGiokS1UaXRajL3ZI3ZlXXuTNxn+vEC048f4xMVYlqRUrtLudmiVG8RS6YI
KHkytQ4mn0IgU2PXKRJNFbGFUnz62WfENB1FL1Fqt3FHYrz/0XX+/N2/5b1rH/Cnf/Eu1z+/gyMQxBsJ
UyjoFHWNXaORW2N3EeMKJ14fy2urCMkkUq6M3upQPT2l2u0TVzUUvUZzcIY1kiGstzn0KERTJZwRlf+8
8SkxNYcnEkPWsgTEOKNf3uXO+DgP5ma5dXec0fGvOfYF8QgxVC1NsZDF7HSxvb+PyWjAYrWye3iEkMgQ
y/4BX2+3qffPSaga8Xyd6tkFlliWYLbNnksinCxz5FaYXlhEymTxBEJkCzrxTI5oQiVVqBJKaDiiCvuO
ILagRFBMEQyHKFWr7FpOOPaEaDZbVOpNjM4QITFNZ3CGXqyQLRZpdntI8QRaucnp5TMkrUi9f0EkkSdV
rOIVM9y+/xAhnSMkKWilEtGkSqpUJ9fqoXWfE6/1cUkFrEEZn5QmGk9S657yaNNCTGtyfvWazsUL/MkS
kUSRy+cv6AzO6fZPqbVapNQMmWqL7rPniJqO3ukTEFXUSp2AnOWXH/+OoJwhmFDRqtUhF7lGn8LZSwpn
V5QuX7PpknEISUKKRrJQoXo6YPTRNsHsKa2Xb6iev+Q4XqPaOOXFy5c0e2e0u130apWwrKBU25T7F/iT
OdKNLiElS67eRUgW+em13xDPFYYaVmm1SOZK5BsDioNX5M9ekbv4hg2nhDOm4lcyZOstOueX3JzZwZJq
o5+9oXLxDcH893hdzi4v6fTPqbc75KoN5KxOotKhcHqOV9HRmgOi6QK5Zh9BrXD9ziSJfBlVL9LodFAL
ZRrnz+i9ek3v9bc0X7xGLp+y6wxj9ktIhTr50wHTuyccSA1c5Wfk+ldECy088RKyXiVdbqFWW0j5Kl4p
w7GQJ17pcxhQiZZ6WCIqgVwDp6xz59Ea6UKNVK4w5Cujl6gPLof4/W/e0nn+hni5z55LwBpMIuSbZNrn
TG05OIpViJ++of7qLZlqh2i+STRXQyo2kCotguk81qCCUSgSq5xxGMoiVM85juUJFTrYY3m+fLSMM5IY
5noyrw97d6HZp/P8isaz1xT6V/gzLQ5cIrZgEqnUIdc6Y8EQ4EisUXn2hubL15Q6A7Rql2y9i9boojXb
hNNZXNEENrlIqjnAGNYIlweYoxqhfAtLWGV8fhN79PvcSqM3Gij5IsXOGb2rb+i8ekv+9DlBtTLULlcs
Q7zcRu+es+WS8GR7tF/9z5CnWu+Meu+S3sUz6r0BjdNTSq02qXwZKVujcDrgWMwSKPYxRLJ41Qb2SJaH
G2Z23SIOIUEoncUbz6BUehT6z9AHVyQaF/hTpWGtilqJ7Pe53O0TSelDXtov3nD57beoxQqVdp/+xSXN
/gXVdge92SWY0IhlK+Q7A+yxLEKlO7y3L1XEHEiweHDCul3g0CdzohRwylnChQ5K6xlK5wq58QK3Uhxi
52sNyo0W/csLEnoFUe9w9fY7Xn33HelCiVqry+DyktbgYsiDJ1kavrUlqiLobbxyhmzrlJNokkSxgiuS
4sG6mVVLkCOPhCWSxhROEi52iTcvkVsviFYvsMtF3KI29Cj5SpVGp0dQSg1r+OXbt7z49u0Qv9LsDLWn
eXaB1ujgz1RwyDqGSAZ/uoZbSJOptvEKKdRyHUdI4YPfjvHx6ASmgMjnEw+5fnMcezRFunVBonmBUB1w
LBe4fuseOyYHeq2O2eFndceCrJXovbyi+/yKZKGGzRPhwGInX2uSrTfRGj3CKR1BLaNVT3FFFJLFBn4x
M1zvFVU+G5/m/uwS9nCMh8vrfD3zBJNfQiy1CGkV/Nkq226JW/dm2DY6KLc6OLwRjM4AYrZMZXBOdfAc
OVfF5hMxurxkCzW0YoHG4Iy0XkTJlym3z/DGUiTLTU5iKtFcGXcsw/U7D3lq8eJRsphOghw5gyzt23BK
GcJqCYeYYXbPxfKeBaMnSq5apzu44CQiY/TGiOarhLQi5qCEyRsb1k+uVEdMp0nkS9h94aFPs4dk7s2t
sW1x8WTbzKPVPb6eXeWj0fssH9mxBWPsmp2sHxzzye17TMyvYw7IrB6d8NGtSRZ3zNhCCRS9SrHeweaN
cGtygR17GF9K58GqgcVtC2qxRqHWxC/F+Wp6kZvjk/zTe+/z6e0Jfvyza/xm9EtGx6ZY3TGwa3bxu7sP
mZhbYf3AwvLWEcvbBn594xY37txnYnGXrxZ2+NWNOyztmFnYNmHxRREzOey+CGNTK9y4t8L0rpXx+W3W
Do6JpfKEldTw77L4dI+Z5afcm11mfuOAielF1g4sLD09IKxoOIMSn9+d4sHCGnOrO8yv7zK7ssX41Bz/
+G/v85f/8DN+8qvrjD1cYm59j40j69Cju8MxvBGJB0tb/NU//4K//vH73J6cx+6P4gxJOAIR5HSGaCJL
OJ5D1atEFA1vSB72UFdIRs7VcIYSjN+b5cjhZNtgHp53eXuf6cV1/uv2BD//8BP+++59HixusrBxwPjk
DKvbh2weWfFHYhjtXn51/RY/+/Azto1WvLE4JmeAA5MdOZ0a9hUhkSOuFnGFk9h9AkExg80fwyPnMZ3E
eLTwhF1HgC2jkxMhwdM9A1Pzy8yt7fHFvVkere0y+XiVLYMNRyjOgd2P8STEltHGnumE67dn+OCLh2xY
TtizurAHBOy+EBFJJqpkiSU0/KLGvj3KnsWL2RVifd/CvjWA0SWwY7By4ImwYXLiEpJYfGFWN/fYtgex
CWlWDC7mN3Yxu/0ch2QMnggeMcmezc3NqVVW7CJP3Uke71o5sPnwigmC8STecBRvREFKZbF7RbaMXnaM
33s2AYPdx57Zi8kjsml0sbBzzOTSNvs2D25JZWPPyNjMErtugbHpJ2weWrB4I0MPsGx0E9Z01o5szK4d
cn/TxhNLiAer+2yY7ITVPFa/gNXlxRkQias5TLYAO4YTNg6dHNmD7Js9rO1bOXJG2Dz2MrN2yPjkEhs7
R3ijce4trDO5uM7Hd+dY3jEwt7qLV9aYX99naeMQOZEkoqSZXNjk1oNVbs9sML22jzUgDH3ijTvTjI5N
YnQEERJZDo89HFi9Q81c3TlmacvC6p6NQ0eYkf/n+N8AAAD//5YD8uy+EAAA
`,
	},

	"/index.html": {
		name:    "index.html",
		local:   "www/index.html",
		size:    373,
		modtime: 1568054952,
		compressed: `
H4sIAAAAAAAC/0yOMW8jIRSE+/0V7151J52D0gNNEildUqRJiWFsSFhYwfPa/vcRtqOkgvnmaWb0n8eX
h7f31yeKMmc76fFQdmVvGIUHgAt20pIkwz4j56rVVUx6hjgqbobhNeG41CZMvhZBEcPHFCSagDV5bC7i
P6WSJLm86d5lmPtR0H1Li1Bv3rCPTu4+OlutrthOOqfySbFhd7N970wN2XCXc0aPgDDJeYFhwUnUOFB2
0uo2fVvDmWrJ1YXviFoKvPz9N/pDWikFwzN6d3v0wVRI6y8rleUgg18+F9RRwraemJbsPGLNAc3wWEER
DUzuIHVX/aGrnzw1ptjpKwAA//9E9glGdQEAAA==
`,
	},

	"/": {
		name:  "/",
		local: `www`,
		isDir: true,
	},
}

var _escDirs = map[string][]os.FileInfo{

	"www": {
		_escData["/chat.css"],
		_escData["/chat.js"],
		_escData["/favicon.ico"],
		_escData["/index.html"],
	},
}