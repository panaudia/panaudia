package space

// type Position = BaseSpace.Position
// type BaseSpace = BaseSpace.BaseSpace
// type Index = BaseSpace.Index
// type CellHandle = BaseSpace.CellHandle

//func TestNewCube(t *testing.T) {
//	c := NewCube("cat", 8, 10.0, 3, false, 2, false, false)
//	//     fmt.Println(c.Depth)
//	if c.Depth != 8 {
//		t.Fatalf(`newCube(name: "cat", depth: 8).depth = %q, want 8, nil`, c.Depth)
//	}
//}
//
//func TestAddNodeToCubeFails(t *testing.T) {
//	c := NewCube("cat", 8, 10.0, 3, false, 2, false, false)
//	err := c.doAddNode("dog", Position{X: 0.5, Y: 0.5, Z: 0.5}, nil)
//	if err == nil {
//		t.Fatalf(`should have errored`)
//	}
//}
//
//func TestAddNodeToCube(t *testing.T) {
//	c := NewCube("cat", 8, 10.0, 3, false, 2, false, false)
//	c.StartChanges()
//	err := c.AddNode("dog", Position{X: 0.5, Y: 0.5, Z: 0.5})
//	if err != nil {
//		t.Fatalf(`should not have errored`)
//	}
//
//	node, _ := c.GetNode("dog")
//	if node.Name != "dog" {
//		t.Fatalf(`c.GetNode("dog").Name = %q, want "dog", nil`, node.Name)
//	}
//
//}
//
//func TestAddTwoNodeToCube(t *testing.T) {
//	c := NewCube("cat", 8, 10.0, 3, false, 2, false, false)
//	c.StartChanges()
//	c.AddNode("dog", Position{X: 0.5, Y: 0.5, Z: 0.5})
//	c.AddNode("cat", Position{X: 0.55, Y: 0.55, Z: 0.55})
//
//}
//
//func TestRemoveANode(t *testing.T) {
//	c := NewCube("cat", 8, 10.0, 3, false, 2, false, false)
//	c.StartChanges()
//	c.AddNode("dog", Position{X: 0.5, Y: 0.5, Z: 0.5})
//	c.AddNode("cat", Position{X: 0.55, Y: 0.55, Z: 0.55})
//
//	c.RemoveNode("dog")
//
//}
//
//func TestMoveANodeToCube(t *testing.T) {
//	c := NewCube("cat", 8, 10.0, 3, false, 2, false, false)
//	c.StartChanges()
//	c.AddNode("dog", Position{X: 0.5, Y: 0.5, Z: 0.5})
//
//	c.MoveNode("dog", Position{X: 0.55, Y: 0.55, Z: 0.55})
//
//}
//
//func Keys[K comparable, V any](m map[K]V) []K {
//	keys := make([]K, 0, len(m))
//	for k := range m {
//		keys = append(keys, k)
//	}
//	return keys
//}
//
//func TestDuplicateName(t *testing.T) {
//	c := NewCube("cat", 8, 10.0, 3, false, 2, false, false)
//	c.StartChanges()
//	error := c.AddNode("dog", Position{X: 0.5, Y: 0.5, Z: 0.5})
//	if error != nil {
//		t.Fatalf(`error error %q`, error)
//	}
//	error = c.doAddNode("dog", Position{X: 0.55, Y: 0.55, Z: 0.55}, nil)
//	if error == nil {
//		t.Fatalf("missing error for TestDuplicateName")
//	}
//}

//
//
//
//func TestAddRemove(t *testing.T) {
//	c := NewCube("cat", 8, 10.0, 3, false, 2, false, false)
//	c.StartChanges()
//	c.AddNode("dog", Position{X: 0.5, Y: 0.5, Z: 0.5})
//	c.AddNode("cat", Position{X: 0.55, Y: 0.55, Z: 0.55})
//	c.RemoveNode("cat")
//
//	//assertAddedLike(t, c, 4, []Index{Index{X: 8, Y: 8, Z: 8}})
//	//assertAddedLike(t, c, 6, []Index{Index{X: 32, Y: 32, Z: 32}})
//	//assertRemovedLike(t, c, 6, []Index{})
//}
//
//func TestAddBreakRemove(t *testing.T) {
//	c := NewCube("cat", 8, 10.0, 3, false, 2, false, false)
//	c.StartChanges()
//	c.AddNode("dog", Position{X: 0.5, Y: 0.5, Z: 0.5})
//	c.AddNode("cat", Position{X: 0.55, Y: 0.55, Z: 0.55})
//	//assertAddedLike(t, c, 6, []Index{Index{X: 32, Y: 32, Z: 32}, Index{X: 35, Y: 35, Z: 35}})
//	c.EndChanges()
//
//	////     fmt.Println("------------")
//	//c.StartChanges()
//	//assertAddedLike(t, c, 6, []Index{})
//	//c.RemoveNode("cat")
//	//assertAddedLike(t, c, 6, []Index{})
//	//assertRemovedLike(t, c, 6, []Index{Index{X: 35, Y: 35, Z: 35}})
//	//c.EndChanges()
//}
//
//func TestRemoveAdd(t *testing.T) {
//	c := NewCube("cat", 8, 10.0, 3, false, 2, false, false)
//	c.StartChanges()
//	c.AddNode("dog", Position{X: 0.5, Y: 0.5, Z: 0.5})
//	c.AddNode("cat", Position{X: 0.55, Y: 0.55, Z: 0.55})
//	c.RemoveNode("cat")
//	//assertAddedLike(t, c, 4, []Index{Index{X: 8, Y: 8, Z: 8}})
//	//assertAddedLike(t, c, 6, []Index{Index{X: 32, Y: 32, Z: 32}})
//}
//
//func TestCellNodes(t *testing.T) {
//	c := NewCube("cat", 8, 10.0, 3, false, 2, false, false)
//	c.StartChanges()
//	c.AddNode("dog", Position{X: 0.5, Y: 0.5, Z: 0.5})
//	c.EndChanges()
//
//	dog, _ := c.GetNode("dog")
//	cell := c.GetCell(dog.CellHandle)
//
//	if cell.Nodes["dog"].Name != "dog" {
//		t.Fatalf("booom")
//	}
//	c.StartChanges()
//	c.MoveNode("dog", Position{X: 0.51, Y: 0.51, Z: 0.51})
//	c.EndChanges()
//
//	cell2 := c.GetCell(dog.CellHandle)
//
//	if cell2.Nodes["dog"].Name != "dog" {
//		t.Fatalf("booom")
//	}
//
//	if cell.Nodes["dog"] != nil {
//		t.Fatalf("booom")
//	}
//}
//
//func TestNeighbours(t *testing.T) {
//	c := NewCube("cat", 8, 10.0, 3, false, 2, false, false)
//	c.StartChanges()
//	c.AddNode("dog", Position{X: 0.5, Y: 0.5, Z: 0.5})
//	c.AddNode("cat", Position{X: 0.51, Y: 0.51, Z: 0.51})
//	c.EndChanges()
//
//	dog, _ := c.GetNode("dog")
//	cat, _ := c.GetNode("cat")
//
//	if dog.GetNeighbourIds()[0] != "cat" {
//		t.Fatalf("booom dog.neighbours")
//	}
//
//	if cat.GetNeighbourIds()[0] != "dog" {
//		t.Fatalf("booom dog.neighbours")
//	}
//}
