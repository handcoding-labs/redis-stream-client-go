# Contributing to Redis Stream Client Go

Thank you for your interest in contributing to Redis Stream Client Go! This document provides guidelines and information for contributors.

## 🚀 Getting Started

### Prerequisites

- Go 1.23 or later
- Docker (for running tests with Redis containers)
- Git

### Development Setup

1. **Fork and Clone**
   ```bash
   git clone https://github.com/YOUR_USERNAME/redis-stream-client-go.git
   cd redis-stream-client-go
   ```

2. **Install Dependencies**
   ```bash
   go mod download
   ```

3. **Run Tests**
   ```bash
   go test -v ./...
   ```

4. **Set Environment Variables**
   For testing, you'll need:
   ```bash
   export POD_NAME=test-consumer-1
   # OR
   export POD_IP=127.0.0.1
   ```

## 🔄 Development Workflow

### Branch Naming Convention

- `feature/description` - New features
- `fix/description` - Bug fixes
- `docs/description` - Documentation updates
- `refactor/description` - Code refactoring
- `test/description` - Test improvements

### Commit Message Format

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```
type(scope): description

[optional body]

[optional footer]
```

**Types:**
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation changes
- `style`: Code style changes (formatting, etc.)
- `refactor`: Code refactoring
- `test`: Adding or updating tests
- `chore`: Maintenance tasks

**Examples:**
```
feat(client): add stream recovery timeout configuration
fix(lbs): handle edge case in message claiming
docs(readme): update usage examples
test(integration): add bulk notification tests
```

## 🧪 Testing Guidelines

### Test Structure

- **Integration Tests**: Located in `test/` directory
- **Unit Tests**: Should be created alongside source files (e.g., `impl/client_test.go`)
- **Benchmarks**: Performance tests for critical paths

### Writing Tests

1. **Test Naming**: Use descriptive names
   ```go
   func TestRedisStreamClient_ClaimHandlesExpiredStream(t *testing.T) {
       // Test implementation
   }
   ```

2. **Use Testcontainers**: For integration tests requiring Redis
   ```go
   redisContainer := setupSuite(t)
   redisClient := newRedisClient(redisContainer)
   ```

3. **Table-Driven Tests**: For multiple test cases
   ```go
   tests := []struct {
       name     string
       input    string
       expected string
   }{
       {"case1", "input1", "expected1"},
       {"case2", "input2", "expected2"},
   }
   ```

### Running Tests

```bash
# Run all tests
go test -v ./...

# Run with coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# Run specific test
go test -v ./test -run TestLBS

# Run benchmarks
go test -bench=. ./...
```

## 📝 Code Style Guidelines

### Go Standards

- Follow [Effective Go](https://golang.org/doc/effective_go.html)
- Use `gofmt` for formatting
- Follow [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)

### Specific Guidelines

1. **Error Handling**
   ```go
   // Good
   if err != nil {
       return fmt.Errorf("failed to claim stream %s: %w", streamName, err)
   }
   
   // Avoid
   if err != nil {
       return err
   }
   ```

2. **Interface Design**
   - Keep interfaces small and focused
   - Define interfaces where they're used, not where they're implemented

3. **Documentation**
   - All exported functions must have godoc comments
   - Include examples for complex functions
   ```go
   // Claim allows a consumer to claim a data stream from another failed consumer.
   // It should be called when a consumer receives a StreamExpired notification.
   //
   // Example:
   //   err := client.Claim(ctx, "session0:1234567890-0")
   //   if err != nil {
   //       slog.Error("Failed to claim stream", "error", err)
   //   }
   func (r *RecoverableRedisStreamClient) Claim(ctx context.Context, kspNotification string) error {
   ```

## 🔍 Code Review Process

### Before Submitting

1. **Self Review**
   - Run `go vet ./...`
   - Run `gofmt -s -w .`
   - Ensure all tests pass
   - Check test coverage

2. **Pull Request Checklist**
   - [ ] Tests added/updated for new functionality
   - [ ] Documentation updated
   - [ ] Commit messages follow convention
   - [ ] No breaking changes (or clearly documented)
   - [ ] Performance impact considered

### Pull Request Template

When creating a PR, include:

```markdown
## Description
Brief description of changes

## Type of Change
- [ ] Bug fix
- [ ] New feature
- [ ] Breaking change
- [ ] Documentation update

## Testing
- [ ] Unit tests added/updated
- [ ] Integration tests pass
- [ ] Manual testing performed

## Checklist
- [ ] Code follows style guidelines
- [ ] Self-review completed
- [ ] Documentation updated
- [ ] Tests added and passing
```

## 🐛 Bug Reports

When reporting bugs, please include:

1. **Environment Information**
   - Go version
   - OS and version
   - Redis version

2. **Steps to Reproduce**
   - Minimal code example
   - Expected vs actual behavior
   - Error messages/logs

3. **Additional Context**
   - Configuration details
   - Network setup
   - Load characteristics

## 💡 Feature Requests

For new features:

1. **Use Case**: Describe the problem you're trying to solve
2. **Proposed Solution**: Your suggested approach
3. **Alternatives**: Other solutions you've considered
4. **Breaking Changes**: Any compatibility concerns

## 📚 Documentation

### Types of Documentation

- **Code Comments**: Explain complex logic
- **README**: Keep examples current
- **Architecture Docs**: System design and flow
- **API Docs**: Generated from godoc comments

### Documentation Standards

- Use clear, concise language
- Include practical examples
- Keep examples up-to-date with code changes
- Explain the "why" not just the "what"

## 🤝 Community Guidelines

### Code of Conduct

- Be respectful and inclusive
- Focus on constructive feedback
- Help newcomers learn and contribute
- Assume positive intent

### Getting Help

- **GitHub Issues**: Bug reports and feature requests
- **GitHub Discussions**: Questions and general discussion
- **Code Review**: Learning opportunity for everyone

## 🏷️ Release Process

### Versioning

We follow [Semantic Versioning](https://semver.org/):
- `MAJOR.MINOR.PATCH`
- Breaking changes increment MAJOR
- New features increment MINOR
- Bug fixes increment PATCH

### Release Checklist

1. Update CHANGELOG.md
2. Update version in relevant files
3. Create release tag
4. Update documentation
5. Announce release

## 📞 Contact

- **Maintainer**: Badari Burli <burli.badari@gmail.com>
- **Issues**: [GitHub Issues](https://github.com/handcoding-labs/redis-stream-client-go/issues)
- **Discussions**: [GitHub Discussions](https://github.com/handcoding-labs/redis-stream-client-go/discussions)

---

Thank you for contributing to Redis Stream Client Go! 🎉
