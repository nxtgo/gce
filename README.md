# gache

a tiny generic in-memory cache for go with optional expiration.  
it runs a background cleaner to drop expired items.  
that's it. nothing fancy.

## features

* 0 deps.
* minimal code (104 lines of code).
* extremely simple to use.

## usage

```go
c := gache.New[string, string](time.Minute)

c.Set("hello", "world", time.Second*30) // or 0 for no expiration.

if v, ok := c.Get("hello"); ok {
    fmt.Println(v) // "world"
}
```

## license

this project is released under CC0 1.0 public domain with
an additional IP waiver. do whatever you want with it.
no rights reserved.
