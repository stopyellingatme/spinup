<!-- doctest: requires-env DATABASE_URL -->
~~~~console
$ echo hello
hello
$ false
[exit 1]
~~~~

<!-- doctest: skip -->
```console
$ echo skipped
```

````text
```console
$ echo not-a-block
```
````

````console
$ printf '```\n'
```
````
