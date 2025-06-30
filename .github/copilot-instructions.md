## Development Guidelines
1. Follow Go best practices and idiomatic patterns.
2. Maintain existing code structure and organization.
3. Ignore any files in `attachments/db/` or `dict` directory.
4. If you want to add unit tests, use `github.com/stretchr/testify` as the testing framework.
5. Run `go build .` before commit to ensure the code compiles, fix any errors if necessary.
6. Run `golangci-lint run` to check for linting issues before commit, and fix any issues if necessary.
