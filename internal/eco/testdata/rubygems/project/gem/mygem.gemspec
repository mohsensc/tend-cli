Gem::Specification.new do |spec|
  spec.name    = "mygem"
  spec.version = "0.3.0"

  spec.add_dependency "thor", "~> 1.3"
  spec.add_runtime_dependency 'zeitwerk', '>= 2.6', '< 3' # autoloading
  spec.add_development_dependency "rake", "~> 13.0"
end
