# Utility Package Guidelines

## Path Trimming

- **Mandatory Trimming**: Configuration strings representing filenames, paths, or identifiers MUST be trimmed of whitespace before comparison or usage to prevent subtle match failures from trailing or leading whitespace in user input or configuration.
