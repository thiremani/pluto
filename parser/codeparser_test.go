package parser

/*
func TestFunctionLiteralParsing(t *testing.T) {
	input := `y, quo = pow(x, n)
    y = 1
    quo = y / x
    y = 0:n x`

	l := lexer.New(input)
	p := New(l, false)
	program := p.ParseProgram()
	checkParserErrors(t, p)

	if len(program.Statements) != 1 {
		t.Fatalf("program.Statements does not contain %d statements. got=%d\n", 1, len(program.Statements))
	}

	stmt, ok := program.Statements[0].(*ast.LetStatement)
	if !ok {
		t.Fatalf("program.Statements[0] is not an ast.ExpressionStatement. got=%T", program.Statements[0])
	}

	if !testIdentifier(t, stmt.Name[0], "y") {
		return
	}

	if !testIdentifier(t, stmt.Name[1], "quo") {
		return
	}

	if len(stmt.Value) != 1 {
		t.Fatalf("stmt does not contain %d value. got=%d\n", 1, len(stmt.Value))
	}

	f, ok := stmt.Value[0].(*ast.FunctionLiteral)
	if !ok {
		t.Fatalf("stmt.Expression is not ast.FunctionLiteral. got=%T",
			stmt.Value[0])
	}

	if len(f.Parameters) != 2 {
		t.Fatalf("function literal parameters wrong. want 2, got=%d\n",
			len(f.Parameters))
	}

	if !testIdentifier(t, f.Parameters[0], "x") {
		return
	}

	if !testIdentifier(t, f.Parameters[1], "n") {
		return
	}

	if len(f.Body.Statements) != 3 {
		t.Fatalf("function literal body has wrong number of statements. want 3, got=%d\n", len(f.Body.Statements))
	}

	stmt1 := f.Body.Statements[0].(*ast.LetStatement)
	if !testIdentifier(t, stmt1.Name[0], "y") {
		return
	}
	if !testIntegerLiteral(t, stmt1.Value[0], 1) {
		return
	}

	stmt3 := f.Body.Statements[2].(*ast.LetStatement)
	if !testIdentifier(t, stmt3.Name[0], "y") {
		return
	}

	if !testInfixExpression(t, stmt3.Condition[0], 0, ":", "n") {
		return
	}

	if !testIdentifier(t, stmt3.Value[0], "x") {
		return
	}
}

func TestFunctionParameterParsing(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{input: `a = fn()
    a = 4`, expected: []string{}},
		{input: `y = f(x)
    y = x * x`, expected: []string{"x"}},
		{input: `r = f(x, y, z)
    r = x + y + z`, expected: []string{"x", "y", "z"}},
	}

	for _, tt := range tests {
		l := lexer.New(tt.input)
		p := New(l, false)
		program := p.ParseProgram()
		checkParserErrors(t, p)

		val := program.Statements[0].(*ast.LetStatement).Value
		function := val[0].(*ast.FunctionLiteral)

		if len(function.Parameters) != len(tt.expected) {
			t.Errorf("length parameters wrong. want %d, got=%d\n",
				len(tt.expected), len(function.Parameters))
		}

		for i, ident := range tt.expected {
			testLiteralExpression(t, function.Parameters[i], ident)
		}
	}
}

*/
