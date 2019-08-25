package astnormalization

import "testing"

func TestResolveInlineFragments(t *testing.T) {
	t.Run("simple", func(t *testing.T) {
		run(mergeInlineFragments, testDefinition, `
					query conflictingBecauseAlias {
						dog {
							... {
								name
							}
							... on Dog {
								nickName
							}
							... {
								... {
									doubleNested
									... on Dog {
										nestedDogName
									}
								}
							}
							extra { string }
							extra { string: noString }
						}
					}`,
			`
					query conflictingBecauseAlias {
						dog {
							name
							nickName
							doubleNested
							nestedDogName
							extra { string }
							extra { string: noString }
						}
					}`)
	})
	t.Run("with interface type", func(t *testing.T) {
		run(mergeInlineFragments, testDefinition, `
					query conflictingBecauseAlias {
						dog {
							... on Pet {
								name
							}
						}
					}`,
			`
					query conflictingBecauseAlias {
						dog {
							name
						}
					}`)
	})
}